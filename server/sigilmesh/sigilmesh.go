// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package sigilmesh builds the fleet-wide sigil
// observability view. Same pattern as echomesh — trait-based
// projection over the standard check-in pipeline — plus two
// sigil-specific signals:
//
//   - **Recipient drift**: if two envoys in the same user's mesh
//     report different pubkey sets (different recipients.txt
//     content), the aggregator flags it. Common cause is one
//     envoy missing a recent peer enrollment, which breaks
//     decryption on that envoy.
//
//   - **Session-helper active**: per-envoy signal of whether the
//     user currently has their sigil identity unlocked in a
//     session-helper process. Ambient operational visibility.
package sigilmesh

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/db"
)

type EnvoyInput struct {
	ID       string
	Hostname string
	Revoked  bool
}

type Fleet struct {
	GeneratedAt  time.Time     `json:"generated_at"`
	Users        []UserSummary `json:"users"`
	Participants []string      `json:"participants"`
	Totals       Totals        `json:"totals"`
}

type Totals struct {
	Users             int `json:"users"`
	Envoys            int `json:"envoys"`
	Files             int `json:"files"`
	Tombstones        int `json:"tombstones"`
	Divergences       int `json:"divergences"`
	RecipientDrifts   int `json:"recipient_drifts"`
	UnlockedSessions  int `json:"unlocked_sessions"`
}

type UserSummary struct {
	User              string          `json:"user"`
	Envoys            []EnvoyPresence `json:"envoys"`
	Files             []FileRow       `json:"files"`
	Tombstones        []TombstoneRow  `json:"tombstones"`
	Divergences       []string        `json:"divergences,omitempty"`
	RecipientDrift    bool            `json:"recipient_drift"`
	ActiveSessions    []string        `json:"active_sessions,omitempty"`
	FileCount         int             `json:"file_count"`
	TotalBytes        int64           `json:"total_bytes"`
}

type EnvoyPresence struct {
	Hostname         string `json:"hostname"`
	Status           string `json:"status"` // reporting | not_reporting | stale
	UpdatedAt        int64  `json:"updated_at,omitempty"`
	FileCount        int    `json:"file_count"`
	FilesDigest      string `json:"files_digest,omitempty"`
	TombstoneCount   int    `json:"tombstone_count"`
	SessionUnlocked  bool   `json:"session_unlocked"`
	// RecipientsDigest is a 16-byte hex digest of this envoy's
	// sorted `(hostname, pubkey, sign_pubkey)` tuples for the
	// user. Drift is flagged when two envoys in the same mesh
	// produce different digests.
	RecipientsDigest string `json:"recipients_digest,omitempty"`
}

type FileRow struct {
	Name     string             `json:"name"`
	Status   string             `json:"status"`
	PerEnvoy []FileCellPerEnvoy `json:"per_envoy"`
}

type FileCellPerEnvoy struct {
	Hostname string `json:"hostname"`
	State    string `json:"state"` // live | tombstoned | missing
	Sha256   string `json:"sha256,omitempty"`
	Size     uint64 `json:"size,omitempty"`
	Mtime    int64  `json:"mtime,omitempty"`
}

type TombstoneRow struct {
	Name         string   `json:"name"`
	DeletionTime int64    `json:"deletion_time"`
	KnownOn      []string `json:"known_on"`
}

// StaleAfter matches echomesh — 5 minutes between the last
// reported trait and "now" flips an envoy to stale.
const StaleAfter = 5 * time.Minute

type Aggregator struct {
	queries *db.Queries

	mu       sync.Mutex
	cache    *Fleet
	cacheExp time.Time
	ttl      time.Duration
}

func NewAggregator(queries *db.Queries) *Aggregator {
	return &Aggregator{queries: queries, ttl: 30 * time.Second}
}

func (a *Aggregator) Snapshot(ctx context.Context, envoys []EnvoyInput) (*Fleet, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache != nil && time.Now().Before(a.cacheExp) {
		return a.cache, nil
	}
	fleet, err := a.build(ctx, envoys)
	if err != nil {
		return nil, err
	}
	a.cache = fleet
	a.cacheExp = time.Now().Add(a.ttl)
	return fleet, nil
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
	totalTombstones := 0
	totalDivergences := 0
	totalDrifts := 0
	totalUnlocked := 0
	for user := range userSet {
		s := buildUserSummary(user, rows, now)
		for _, ep := range s.Envoys {
			participants[ep.Hostname] = struct{}{}
			if ep.SessionUnlocked {
				totalUnlocked++
			}
		}
		totalFiles += s.FileCount
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
			Users:            len(summaries),
			Envoys:           len(partList),
			Files:            totalFiles,
			Tombstones:       totalTombstones,
			Divergences:      totalDivergences,
			RecipientDrifts:  totalDrifts,
			UnlockedSessions: totalUnlocked,
		},
	}, nil
}

func buildUserSummary(user string, rows []envRow, now time.Time) UserSummary {
	summary := UserSummary{User: user}

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
			presence.FilesDigest = us.FilesDigest
			presence.TombstoneCount = us.TombstoneCount
			presence.SessionUnlocked = us.SessionUnlocked
			presence.RecipientsDigest = recipientsDigest(us.Peers)
			digests[presence.RecipientsDigest] = struct{}{}

			if presence.SessionUnlocked {
				summary.ActiveSessions = append(summary.ActiveSessions, r.hostname)
			}
			for _, f := range us.Files {
				summary.Files = appendFileCell(
					summary.Files, r.hostname, f.Name, f.Sha256, f.Size, f.Mtime, "live")
			}
			for _, t := range us.Tombstones {
				summary.Files = appendFileCell(
					summary.Files, r.hostname, t.Name, "", 0, t.DeletionTime, "tombstoned")
				summary.Tombstones = mergeTombstone(summary.Tombstones, t.Name, t.DeletionTime, r.hostname)
			}
		}
		summary.Envoys = append(summary.Envoys, presence)
	}
	sort.Slice(summary.Envoys, func(i, j int) bool {
		return summary.Envoys[i].Hostname < summary.Envoys[j].Hostname
	})
	sort.Strings(summary.ActiveSessions)

	// Recipient drift if there are ≥2 distinct digests from
	// reporting envoys. Empty digest (fresh envoy with no peers
	// yet) still counts as "a value" — that's deliberate: an
	// envoy that hasn't learned anyone is in a different state
	// from one that has, and drift should surface that.
	summary.RecipientDrift = len(digests) >= 2

	finalizeFiles(&summary)
	finalizeTombstones(&summary)
	return summary
}

func finalizeTombstones(summary *UserSummary) {
	for i := range summary.Tombstones {
		sort.Strings(summary.Tombstones[i].KnownOn)
	}
	sort.Slice(summary.Tombstones, func(i, j int) bool {
		return summary.Tombstones[i].Name < summary.Tombstones[j].Name
	})
}

func appendFileCell(files []FileRow, hostname, name, sha string, size uint64, mtime int64, state string) []FileRow {
	for i := range files {
		if files[i].Name == name {
			files[i].PerEnvoy = append(files[i].PerEnvoy, FileCellPerEnvoy{
				Hostname: hostname, State: state, Sha256: sha, Size: size, Mtime: mtime,
			})
			return files
		}
	}
	return append(files, FileRow{
		Name: name,
		PerEnvoy: []FileCellPerEnvoy{{
			Hostname: hostname, State: state, Sha256: sha, Size: size, Mtime: mtime,
		}},
	})
}

func mergeTombstone(rows []TombstoneRow, name string, deletionTime int64, hostname string) []TombstoneRow {
	for i := range rows {
		if rows[i].Name == name {
			rows[i].KnownOn = append(rows[i].KnownOn, hostname)
			if deletionTime > rows[i].DeletionTime {
				rows[i].DeletionTime = deletionTime
			}
			return rows
		}
	}
	return append(rows, TombstoneRow{Name: name, DeletionTime: deletionTime, KnownOn: []string{hostname}})
}

func finalizeFiles(summary *UserSummary) {
	summary.FileCount = 0
	summary.TotalBytes = 0
	for i := range summary.Files {
		f := &summary.Files[i]
		seen := make(map[string]struct{}, len(f.PerEnvoy))
		for _, c := range f.PerEnvoy {
			seen[c.Hostname] = struct{}{}
		}
		for _, ep := range summary.Envoys {
			if ep.Status != "reporting" {
				continue
			}
			if _, ok := seen[ep.Hostname]; !ok {
				f.PerEnvoy = append(f.PerEnvoy, FileCellPerEnvoy{
					Hostname: ep.Hostname, State: "missing",
				})
			}
		}
		sort.Slice(f.PerEnvoy, func(a, b int) bool {
			return f.PerEnvoy[a].Hostname < f.PerEnvoy[b].Hostname
		})

		liveCount := 0
		tombCount := 0
		missingCount := 0
		shas := make(map[string]struct{})
		var maxSize uint64
		for _, c := range f.PerEnvoy {
			switch c.State {
			case "live":
				liveCount++
				if c.Sha256 != "" {
					shas[c.Sha256] = struct{}{}
				}
				if c.Size > maxSize {
					maxSize = c.Size
				}
			case "tombstoned":
				tombCount++
			case "missing":
				missingCount++
			}
		}
		switch {
		case tombCount > 0 && liveCount == 0:
			f.Status = "tombstoned"
		case liveCount > 0 && len(shas) > 1:
			f.Status = "divergent"
			summary.Divergences = append(summary.Divergences, f.Name)
		case missingCount > 0 && liveCount > 0:
			f.Status = "partial"
			summary.Divergences = append(summary.Divergences, f.Name)
		default:
			f.Status = "live"
		}
		if liveCount > 0 {
			summary.FileCount++
			summary.TotalBytes += int64(maxSize)
		}
	}
	sort.Slice(summary.Files, func(i, j int) bool { return summary.Files[i].Name < summary.Files[j].Name })
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

func (a *Aggregator) loadTrait(ctx context.Context, envoyID string) (*traitEnvelope, bool) {
	tr, err := a.queries.GetLatestEnvoyTraits(ctx, envoyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		return nil, false
	}
	var outer struct {
		Sigil *traitEnvelope `json:"sigil"`
	}
	if err := json.Unmarshal([]byte(tr.Traits), &outer); err != nil {
		return nil, false
	}
	if outer.Sigil == nil {
		return nil, false
	}
	return outer.Sigil, true
}

// --- trait payload shapes (agent side) ---

type traitEnvelope struct {
	UpdatedAt int64       `json:"updated_at"`
	Users     []userTrait `json:"users"`
}

type userTrait struct {
	User            string           `json:"user"`
	FileCount       int              `json:"file_count"`
	TotalBytes      int64            `json:"total_bytes"`
	Files           []fileTrait      `json:"files"`
	FilesDigest     string           `json:"files_digest"`
	TombstoneCount  int              `json:"tombstone_count"`
	Tombstones      []tombstoneTrait `json:"tombstones"`
	Peers           []peerTrait      `json:"peers"`
	SessionUnlocked bool             `json:"session_unlocked"`
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
