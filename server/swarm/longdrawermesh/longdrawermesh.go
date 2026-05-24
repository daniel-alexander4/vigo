// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package longdrawermesh builds the fleet-wide longdrawer observability
// view by joining per-user snapshots from every envoy's latest
// `longdrawer` trait. Mirrors the gitbackmesh pattern — pure
// projection over the standard trait pipeline, no new RPC.
//
// Longdrawer is plaintext LAN-only peer sync. This aggregator exists
// purely so operators with admin access can see which users are in
// which mesh, spot file-set divergence, and diagnose sync failures
// without shelling into every envoy.
package longdrawermesh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm"
)

// EnvoyInput decouples this package from server/fleet so both the
// UI and REST handler can consume it without an import cycle.
type EnvoyInput struct {
	ID       string
	Hostname string
	Revoked  bool
}

// Fleet is the top-level aggregated view.
type Fleet struct {
	GeneratedAt  time.Time     `json:"generated_at"`
	Users        []UserSummary `json:"users"`
	Participants []string      `json:"participants"`
	Totals       Totals        `json:"totals"`
}

// Totals across all users and envoys.
type Totals struct {
	Users        int `json:"users"`
	Envoys       int `json:"envoys"`
	Files        int `json:"files"`
	Tombstones   int `json:"tombstones"`
	Divergences  int `json:"divergences"`
}

// UserSummary rolls up one user's state across every envoy they're
// enrolled on.
type UserSummary struct {
	User       string          `json:"user"`
	Envoys     []EnvoyPresence `json:"envoys"`
	Files      []FileRow       `json:"files"`
	Tombstones []TombstoneRow  `json:"tombstones"`
	// Divergence is non-empty when at least one (envoy × file)
	// pair disagrees with the modal state (different sha, missing
	// entirely, or mtime skew beyond threshold).
	Divergences []string `json:"divergences,omitempty"`
	FileCount   int      `json:"file_count"`
	TotalBytes  int64    `json:"total_bytes"`
}

// EnvoyPresence is one envoy's stance on a user's mesh. Reporting,
// not-reporting, and stale are distinct states so the UI can tell
// rolling-upgrade envoys apart from dead ones.
type EnvoyPresence struct {
	Hostname     string `json:"hostname"`
	Status       string `json:"status"` // reporting | not_reporting | stale
	UpdatedAt    int64  `json:"updated_at,omitempty"`
	FileCount    int    `json:"file_count"`
	FilesDigest  string `json:"files_digest,omitempty"`
	TombstoneCount int  `json:"tombstone_count"`
}

// FileRow joins one filename across every envoy that knows about
// it. Status is per-(user,filename): "live" if every reporting
// envoy has it with a matching sha, "divergent" if sha disagrees,
// "partial" if some envoys have it and some don't, "tombstoned"
// if every reporting envoy has a tombstone for it.
type FileRow struct {
	Name    string            `json:"name"`
	Status  string            `json:"status"` // live | divergent | partial | tombstoned
	PerEnvoy []FileCellPerEnvoy `json:"per_envoy"`
}

type FileCellPerEnvoy struct {
	Hostname string `json:"hostname"`
	State    string `json:"state"` // live | tombstoned | missing
	Sha256   string `json:"sha256,omitempty"`
	Size     uint64 `json:"size,omitempty"`
	Mtime    int64  `json:"mtime,omitempty"`
}

// TombstoneRow summarizes a deletion propagation across the mesh.
type TombstoneRow struct {
	Name         string   `json:"name"`
	DeletionTime int64    `json:"deletion_time"`
	KnownOn      []string `json:"known_on"` // envoys that hold this tombstone
}

// Staleness cutoff — an envoy whose most recent longdrawer trait is
// older than this is reported as `stale`. Two missed announce
// cycles (longdrawer's reconciler runs every 30s) gives tolerance
// for a single missed check-in without a flap.
const StaleAfter = 5 * time.Minute

// Aggregator carries a short cache so HTMX drill-down fragments
// loaded off a single page render don't re-parse every envoy's
// trait. Matches gitbackmesh's design.
type Aggregator struct {
	queries *db.Queries

	mu       sync.Mutex
	cache    *Fleet
	cacheExp time.Time
	ttl      time.Duration
}

func NewAggregator(queries *db.Queries) *Aggregator {
	return &Aggregator{queries: queries, ttl: swarm.MeshCacheTTL}
}

// Snapshot returns cached fleet view or rebuilds.
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

// FullFileList pulls the full (not bounded to 20) file list for
// one (user × envoy) cell. Used by the UI drill-down when an
// operator clicks a user row to inspect file-by-file state.
// Returns (nil, nil) when the envoy didn't report or hasn't been
// enrolled for that user.
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

// envRow is the internal shape used while building a Fleet snapshot.
type envRow struct {
	hostname string
	id       string
	envelope *traitEnvelope
	reported bool
}

// build walks the full envoy list, pulls each one's longdrawer
// trait, and constructs the cross-envoy joined view.
func (a *Aggregator) build(ctx context.Context, envoys []EnvoyInput) (*Fleet, error) {
	rows := make([]envRow, 0, len(envoys))
	now := time.Now()
	for _, e := range envoys {
		if e.Revoked {
			continue
		}
		env, ok := a.loadTrait(ctx, e.ID)
		rows = append(rows, envRow{
			hostname: e.Hostname,
			id:       e.ID,
			envelope: env,
			reported: ok,
		})
	}

	// Collect user set. A user exists on the mesh if at least one
	// envoy reports state for them.
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
	for user := range userSet {
		summary := buildUserSummary(user, rows, now)
		for _, ep := range summary.Envoys {
			participants[ep.Hostname] = struct{}{}
		}
		totalFiles += summary.FileCount
		totalTombstones += len(summary.Tombstones)
		totalDivergences += len(summary.Divergences)
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].User < summaries[j].User })

	partList := make([]string, 0, len(participants))
	for p := range participants {
		partList = append(partList, p)
	}
	sort.Strings(partList)

	fleet := &Fleet{
		GeneratedAt:  now,
		Users:        summaries,
		Participants: partList,
		Totals: Totals{
			Users:       len(summaries),
			Envoys:      len(partList),
			Files:       totalFiles,
			Tombstones:  totalTombstones,
			Divergences: totalDivergences,
		},
	}
	return fleet, nil
}

func buildUserSummary(user string, rows []envRow, now time.Time) UserSummary {
	summary := UserSummary{User: user}

	// Per-envoy presence. Include every envoy — reporting or not
	// — so rolling-upgrade gaps are visible.
	for _, r := range rows {
		presence := EnvoyPresence{Hostname: r.hostname, Status: "not_reporting"}
		if r.reported {
			// Find this user's snapshot.
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
			// Contribute to the file row index below.
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

	// Post-process: compute per-file status + divergence list;
	// fill missing cells with "missing"; compute fileCount.
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
	// Count total unique files.
	summary.FileCount = 0
	for i := range summary.Files {
		f := &summary.Files[i]
		// Fill missing envoys for completeness.
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

		// Derive status.
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

// EnvoyFile is one longdrawer file present on a given envoy, projected
// from the per-user `users[].files[]` field for the per-envoy Swarm
// Load page. Path is the user-friendly render (`~<user>/longdrawer/<name>`)
// so the UI never surfaces an internal key.
type EnvoyFile struct {
	User  string
	Name  string
	Path  string
	Size  uint64
	Mtime int64
}

// FilesOnEnvoy returns every longdrawer file this envoy holds across every
// user with state on it, in user+name order. Used by the per-envoy
// Swarm Load page to rank disk consumers. Returns nil when the
// envoy has no longdrawer trait.
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
				Path:  "~" + u.User + "/longdrawer/" + f.Name,
				Size:  f.Size,
				Mtime: f.Mtime,
			})
		}
	}
	return out
}

// loadTrait pulls the latest envoy traits row and extracts the
// `longdrawer` namespace. Missing / malformed / absent namespace all
// collapse to `(nil, false)` — treated as "not reporting."
func (a *Aggregator) loadTrait(ctx context.Context, envoyID string) (*traitEnvelope, bool) {
	tr, err := a.queries.GetLatestEnvoyTraits(ctx, envoyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		return nil, false
	}
	var outer struct {
		Longdrawer *traitEnvelope `json:"longdrawer"`
	}
	if err := json.Unmarshal([]byte(tr.Traits), &outer); err != nil {
		return nil, false
	}
	if outer.Longdrawer == nil {
		return nil, false
	}
	return outer.Longdrawer, true
}

// --- trait payload shapes (agent side, deserialized here) ---

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
