// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package lockboxmesh builds the fleet-wide lockbox
// observability view. Same pattern as longdrawermesh — trait-based
// projection over the standard check-in pipeline — plus the
// lockbox-specific **recipient drift** signal: if two envoys in the
// same user's mesh report different pubkey sets (different
// recipients.txt content), the aggregator flags it. Common cause is
// one envoy missing a recent peer enrollment, which breaks decryption
// on that envoy.
//
// Puddle session-unlock state is NOT reported here — it's a puddle
// fact, surfaced on the puddle view via `puddlemesh` (the puddle trait
// is its sole reporter).
package lockboxmesh

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm"
	"git.alexander4.io/vigo/server/swarm/filemeshcore"
)

// EnvoyInput, FileRow, FileCellPerEnvoy, and TombstoneRow are the
// file-mesh shapes shared with longdrawermesh — aliased from
// filemeshcore, which owns the status-derivation machine. Lockbox's
// own deltas (recipient drift, failed files) live on the local
// Fleet/UserSummary/EnvoyPresence shapes below.
type (
	EnvoyInput       = filemeshcore.EnvoyInput
	FileRow          = filemeshcore.FileRow
	FileCellPerEnvoy = filemeshcore.FileCellPerEnvoy
	TombstoneRow     = filemeshcore.TombstoneRow
)

type Fleet struct {
	GeneratedAt  time.Time     `json:"generated_at"`
	Users        []UserSummary `json:"users"`
	Participants []string      `json:"participants"`
	Totals       Totals        `json:"totals"`
}

type Totals struct {
	Users           int `json:"users"`
	Envoys          int `json:"envoys"`
	Files           int `json:"files"`
	Tombstones      int `json:"tombstones"`
	Divergences     int `json:"divergences"`
	FailedFiles     int `json:"failed_files"`
	RecipientDrifts int `json:"recipient_drifts"`
}

type UserSummary struct {
	User           string          `json:"user"`
	Envoys         []EnvoyPresence `json:"envoys"`
	Files          []FileRow       `json:"files"`
	Tombstones     []TombstoneRow  `json:"tombstones"`
	Divergences    []string        `json:"divergences,omitempty"`
	RecipientDrift bool            `json:"recipient_drift"`
	FileCount      int             `json:"file_count"`
	FailedFiles    int             `json:"failed_files"`
	TotalBytes     int64           `json:"total_bytes"`
}

type EnvoyPresence struct {
	Hostname        string `json:"hostname"`
	Status          string `json:"status"` // reporting | not_reporting | stale
	UpdatedAt       int64  `json:"updated_at,omitempty"`
	FileCount       int    `json:"file_count"`
	FailedFileCount int    `json:"failed_file_count"`
	FilesDigest     string `json:"files_digest,omitempty"`
	TombstoneCount  int    `json:"tombstone_count"`
	// RecipientsDigest is a 16-byte hex digest of this envoy's
	// sorted `(hostname, pubkey, sign_pubkey)` tuples for the
	// user. Drift is flagged when two envoys in the same mesh
	// produce different digests.
	RecipientsDigest string `json:"recipients_digest,omitempty"`
}

// StaleAfter matches longdrawermesh — 5 minutes between the last
// reported trait and "now" flips an envoy to stale.
const StaleAfter = 5 * time.Minute

type Aggregator struct {
	queries *db.Queries

	// cache rebuilds outside its lock (swarm.MeshCache): a cache hit never
	// waits behind a fleet-wide trait scan.
	cache *swarm.MeshCache[*Fleet]
}

func NewAggregator(queries *db.Queries) *Aggregator {
	return &Aggregator{queries: queries, cache: swarm.NewMeshCache[*Fleet](swarm.MeshCacheTTL)}
}

func (a *Aggregator) Snapshot(ctx context.Context, envoys []EnvoyInput) (*Fleet, error) {
	return a.cache.Get(func() (*Fleet, error) { return a.build(ctx, envoys) })
}

// FullFileList — UI drill-down for one (user × envoy) cell.
func (a *Aggregator) FullFileList(ctx context.Context, envoyID, user string) ([]FileRow, error) {
	env, ok := a.loadTrait(ctx, envoyID)
	if !ok {
		return nil, nil
	}
	for _, u := range env.Users {
		if u.User != user {
			continue
		}
		out := make([]FileRow, 0, len(u.Files))
		for _, f := range u.Files {
			out = append(out, FileRow{
				Name:   f.Name,
				Status: "live",
				PerEnvoy: []FileCellPerEnvoy{{
					State:  "live",
					Sha256: f.Sha256,
					Size:   f.Size,
					Mtime:  f.Mtime,
				}},
			})
		}
		return out, nil
	}
	return nil, nil
}

type envRow struct {
	hostname string
	id       string
	envelope *traitEnvelope
	reported bool
}

func (a *Aggregator) build(ctx context.Context, envoys []EnvoyInput) (*Fleet, error) {
	rows := make([]envRow, 0, len(envoys))
	now := time.Now()
	for _, e := range envoys {
		if e.Revoked {
			continue
		}
		env, ok := a.loadTrait(ctx, e.ID)
		rows = append(rows, envRow{
			hostname: e.Hostname, id: e.ID, envelope: env, reported: ok,
		})
	}

	userSet := make(map[string]struct{})
	for _, r := range rows {
		if !r.reported {
			continue
		}
		for _, u := range r.envelope.Users {
			userSet[u.User] = struct{}{}
		}
	}

	summaries := make([]UserSummary, 0, len(userSet))
	participants := make(map[string]struct{})
	totalFiles := 0
	totalFailed := 0
	totalTombstones := 0
	totalDivergences := 0
	totalDrifts := 0
	for user := range userSet {
		s := buildUserSummary(user, rows, now)
		for _, ep := range s.Envoys {
			participants[ep.Hostname] = struct{}{}
		}
		totalFiles += s.FileCount
		totalFailed += s.FailedFiles
		totalTombstones += len(s.Tombstones)
		totalDivergences += len(s.Divergences)
		if s.RecipientDrift {
			totalDrifts++
		}
		summaries = append(summaries, s)
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].User < summaries[j].User })

	partList := make([]string, 0, len(participants))
	for p := range participants {
		partList = append(partList, p)
	}
	sort.Strings(partList)

	return &Fleet{
		GeneratedAt:  now,
		Users:        summaries,
		Participants: partList,
		Totals: Totals{
			Users:           len(summaries),
			Envoys:          len(partList),
			Files:           totalFiles,
			FailedFiles:     totalFailed,
			Tombstones:      totalTombstones,
			Divergences:     totalDivergences,
			RecipientDrifts: totalDrifts,
		},
	}, nil
}

func buildUserSummary(user string, rows []envRow, now time.Time) UserSummary {
	summary := UserSummary{User: user}
	var idx filemeshcore.Index

	// Collect digests to detect recipient drift.
	digests := make(map[string]struct{})

	for _, r := range rows {
		presence := EnvoyPresence{Hostname: r.hostname, Status: "not_reporting"}
		if r.reported {
			var us *userTrait
			for i := range r.envelope.Users {
				if r.envelope.Users[i].User == user {
					us = &r.envelope.Users[i]
					break
				}
			}
			if us == nil {
				continue
			}
			if now.Unix()-r.envelope.UpdatedAt > int64(StaleAfter.Seconds()) {
				presence.Status = "stale"
			} else {
				presence.Status = "reporting"
			}
			presence.UpdatedAt = r.envelope.UpdatedAt
			presence.FileCount = us.FileCount
			presence.FailedFileCount = us.FailedFileCount
			summary.FailedFiles += us.FailedFileCount
			presence.FilesDigest = us.FilesDigest
			presence.TombstoneCount = us.TombstoneCount
			presence.RecipientsDigest = recipientsDigest(us.Peers)
			digests[presence.RecipientsDigest] = struct{}{}

			for _, f := range us.Files {
				idx.AddFileCell(r.hostname, f.Name, f.Sha256, f.Size, f.Mtime, "live")
			}
			for _, t := range us.Tombstones {
				idx.AddFileCell(r.hostname, t.Name, "", 0, t.DeletionTime, "tombstoned")
				idx.MergeTombstone(t.Name, t.DeletionTime, r.hostname)
			}
		}
		summary.Envoys = append(summary.Envoys, presence)
	}
	sort.Slice(summary.Envoys, func(i, j int) bool {
		return summary.Envoys[i].Hostname < summary.Envoys[j].Hostname
	})

	// Recipient drift if there are ≥2 distinct digests from
	// reporting envoys. Empty digest (fresh envoy with no peers
	// yet) still counts as "a value" — that's deliberate: an
	// envoy that hasn't learned anyone is in a different state
	// from one that has, and drift should surface that.
	summary.RecipientDrift = len(digests) >= 2

	// The shared file-mesh machine (status + divergence + counts).
	summary.Files, summary.Tombstones = idx.Files, idx.Tombstones
	summary.FileCount, summary.TotalBytes, summary.Divergences =
		filemeshcore.FinalizeFiles(summary.Files, reportingHosts(summary.Envoys))
	filemeshcore.FinalizeTombstones(summary.Tombstones)
	return summary
}

// reportingHosts is the hostnames whose presence status is "reporting"
// — the envoys against which a file's absence counts as "missing".
func reportingHosts(envoys []EnvoyPresence) []string {
	hosts := make([]string, 0, len(envoys))
	for _, ep := range envoys {
		if ep.Status == "reporting" {
			hosts = append(hosts, ep.Hostname)
		}
	}
	return hosts
}

// recipientsDigest computes a 16-byte hex digest over sorted
// `(hostname, pubkey, sign_pubkey)` tuples. Two envoys with
// identical peer views produce identical digests regardless of
// peer-table insertion order.
func recipientsDigest(peers []peerTrait) string {
	sorted := make([]peerTrait, len(peers))
	copy(sorted, peers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Hostname < sorted[j].Hostname })
	h := sha256.New()
	for _, p := range sorted {
		h.Write([]byte(p.Hostname))
		h.Write([]byte{0})
		h.Write([]byte(p.Pubkey))
		h.Write([]byte{0})
		h.Write([]byte(p.SignPubkey))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// EnvoyFile is one lockbox ciphertext present on a given envoy,
// projected from the per-user `users[].files[]` field for the
// per-envoy Swarm Load page. Path is the user-friendly render
// (`~<user>/lockbox/<name>`) — lockbox holds encrypted bytes but the
// on-disk name is still the one the user set.
type EnvoyFile struct {
	User  string
	Name  string
	Path  string
	Size  uint64
	Mtime int64
}

// FilesOnEnvoy returns every lockbox file this envoy holds across
// every user with state on it, in user+name order. Used by the
// per-envoy Swarm Load page to rank disk consumers. Returns nil
// when the envoy has no lockbox trait.
func (a *Aggregator) FilesOnEnvoy(ctx context.Context, envoyID string) []EnvoyFile {
	env, ok := a.loadTrait(ctx, envoyID)
	if !ok {
		return nil
	}
	var out []EnvoyFile
	for _, u := range env.Users {
		for _, f := range u.Files {
			out = append(out, EnvoyFile{
				User:  u.User,
				Name:  f.Name,
				Path:  "~" + u.User + "/lockbox/" + f.Name,
				Size:  f.Size,
				Mtime: f.Mtime,
			})
		}
	}
	return out
}

func (a *Aggregator) loadTrait(ctx context.Context, envoyID string) (*traitEnvelope, bool) {
	tr, err := a.queries.GetLatestEnvoyTraits(ctx, envoyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		return nil, false
	}
	var outer struct {
		Lockbox *traitEnvelope `json:"lockbox"`
	}
	if err := json.Unmarshal([]byte(tr.Traits), &outer); err != nil {
		return nil, false
	}
	if outer.Lockbox == nil {
		return nil, false
	}
	return outer.Lockbox, true
}

// --- trait payload shapes (agent side) ---

type traitEnvelope struct {
	UpdatedAt int64       `json:"updated_at"`
	Users     []userTrait `json:"users"`
}

type userTrait struct {
	User            string           `json:"user"`
	FileCount       int              `json:"file_count"`
	FailedFileCount int              `json:"failed_file_count"`
	TotalBytes      int64            `json:"total_bytes"`
	Files           []fileTrait      `json:"files"`
	FilesDigest     string           `json:"files_digest"`
	TombstoneCount  int              `json:"tombstone_count"`
	Tombstones      []tombstoneTrait `json:"tombstones"`
	Peers           []peerTrait      `json:"peers"`
}

type fileTrait struct {
	Name   string `json:"name"`
	Size   uint64 `json:"size"`
	Mtime  int64  `json:"mtime"`
	Sha256 string `json:"sha256"`
}

type tombstoneTrait struct {
	Name         string `json:"name"`
	DeletionTime int64  `json:"deletion_time"`
}

type peerTrait struct {
	Hostname   string `json:"hostname"`
	Pubkey     string `json:"pubkey"`
	SignPubkey string `json:"sign_pubkey"`
	LastSeen   int64  `json:"last_seen"`
}
