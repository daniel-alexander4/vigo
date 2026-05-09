// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package swarmview assembles the fleet-wide substrate-overview
// rollup used by the admin surfaces (`vigocli swarm`, the `/swarm`
// web page, the `/api/v1/swarm/*` REST endpoints).
//
// Substrate state is already kept in server/swarm (PeerTracker,
// ManifestStore) and server/fleet (FleetIndex); this package owns the
// join. Types + snapshot functions live here so `server/api` and
// `ui/` can both consume them without creating an import cycle — the
// same split the content-subsystem aggregators use
// (swarmgitmesh/longdrawermesh/lockboxmesh).
//
// All functions are pure reads over already-in-memory data; nothing
// here runs on the agent check-in hot path.
package swarmview

import (
	"sort"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/swarm"
)

// EnvoyInput is the minimal per-envoy shape swarmview needs from the
// fleet index. Declared here so this package doesn't import
// server/fleet — matches the pattern in server/swarmgitmesh and
// server/longdrawermesh. Callers convert fleet.EnvoyState → EnvoyInput
// before calling Snapshot*.
type EnvoyInput struct {
	ID       string
	Hostname string
	LastSeen time.Time
	Revoked  bool
	Offline  bool
}

// Status is the fleet-wide substrate overview.
type Status struct {
	GeneratedAt            time.Time     `json:"generated_at"`
	EnvoysTotal            int           `json:"envoys_total"`
	EnvoysOnline           int           `json:"envoys_online"`
	EnvoysSubstrateActive  int           `json:"envoys_substrate_active"`
	EnvoysSeeding          int           `json:"envoys_seeding"`
	ManifestEntriesActive  int           `json:"manifest_entries_active"`
	ManifestEntriesRevoked int           `json:"manifest_entries_revoked"`
	BlobsByKind            BlobCounts `json:"blobs_by_kind"`
	TotalFootprintBytes    int64      `json:"total_footprint_bytes"`
}

// BlobCounts breaks active seeds out by content subsystem.
type BlobCounts struct {
	Swarmcast int `json:"swarmcast"`
	Swarmgit  int `json:"swarmgit"`
}

// Peers is the per-envoy rollup.
type Peers struct {
	GeneratedAt time.Time `json:"generated_at"`
	Total       int       `json:"total"`
	Returned    int       `json:"returned"`
	Sort        string    `json:"sort"`
	Limit       int       `json:"limit"`
	Envoys      []PeerRow `json:"envoys"`
}

// PeerRow is one envoy's substrate-participation counts.
type PeerRow struct {
	Hostname         string    `json:"hostname"`
	ID               string    `json:"id"`
	LastSeen         time.Time `json:"last_seen"`
	Online           bool      `json:"online"`
	SubstrateActive  bool      `json:"substrate_active"`
	SeedingCount     int       `json:"seeding_count"`
	CachedCount      int       `json:"cached_count"`
	DownloadingCount int       `json:"downloading_count"`
	FootprintBytes   int64     `json:"footprint_bytes"`
}

// Sources bundles the read handles swarmview needs. Kept as an
// explicit struct rather than a free-function arglist so the callers
// (server/api, ui) can pass one value around. FleetSource is a
// function so the caller can convert from fleet.EnvoyState lazily
// without forcing a cycle back into server/fleet.
type Sources struct {
	Tracker       *swarm.PeerTracker
	ManifestStore *swarm.ManifestStore
	FleetSource   func() []EnvoyInput
}

// SnapshotStatus builds the fleet-wide status rollup.
func SnapshotStatus(src Sources) Status {
	out := Status{
		GeneratedAt: time.Now().UTC(),
	}

	// Seed totals split by content-subsystem label prefix and size
	// table for the footprint join.
	seedSize := make(map[string]uint64)
	seedingEnvoys := make(map[string]bool)
	if src.Tracker != nil {
		for sha, seed := range src.Tracker.ListSeeds() {
			seedSize[sha] = seed.Size
			if strings.HasPrefix(seed.Label, "swarmgit:") {
				out.BlobsByKind.Swarmgit++
			} else {
				out.BlobsByKind.Swarmcast++
			}
			if seed.SeederID != "" {
				seedingEnvoys[seed.SeederID] = true
			}
		}
	}

	if src.ManifestStore != nil {
		for _, e := range src.ManifestStore.Entries() {
			if e.Status == "revoked" {
				out.ManifestEntriesRevoked++
			} else {
				out.ManifestEntriesActive++
			}
		}
	}

	active := make(map[string]bool)
	footprint := int64(0)
	if src.Tracker != nil {
		src.Tracker.ForEachPeer(func(sha, envoyID string, complete bool) {
			active[envoyID] = true
			if complete {
				if size, ok := seedSize[sha]; ok {
					footprint += int64(size)
				}
			}
		})
	}
	for id := range seedingEnvoys {
		active[id] = true
	}
	out.EnvoysSubstrateActive = len(active)
	out.EnvoysSeeding = len(seedingEnvoys)
	out.TotalFootprintBytes = footprint

	if src.FleetSource != nil {
		for _, e := range src.FleetSource() {
			if e.Revoked {
				continue
			}
			out.EnvoysTotal++
			if !e.Offline {
				out.EnvoysOnline++
			}
		}
	}
	return out
}

// SnapshotPeers builds the per-envoy rollup. Sort keys: "footprint"
// (default), "blobs", "hostname". A limit of 0 means "no cap".
func SnapshotPeers(src Sources, sortKey string, limit int) Peers {
	out := Peers{
		GeneratedAt: time.Now().UTC(),
		Sort:        sortKey,
		Limit:       limit,
	}
	if src.FleetSource == nil {
		return out
	}

	seedSize := make(map[string]uint64)
	seedersByEnvoy := make(map[string]int)
	if src.Tracker != nil {
		for sha, seed := range src.Tracker.ListSeeds() {
			seedSize[sha] = seed.Size
			if seed.SeederID != "" {
				seedersByEnvoy[seed.SeederID]++
			}
		}
	}

	type perEnvoy struct {
		cached      int
		downloading int
		footprint   int64
	}
	rollup := make(map[string]*perEnvoy)
	if src.Tracker != nil {
		src.Tracker.ForEachPeer(func(sha, envoyID string, complete bool) {
			pe := rollup[envoyID]
			if pe == nil {
				pe = &perEnvoy{}
				rollup[envoyID] = pe
			}
			if complete {
				pe.cached++
				if size, ok := seedSize[sha]; ok {
					pe.footprint += int64(size)
				}
			} else {
				pe.downloading++
			}
		})
	}

	envoys := src.FleetSource()
	rows := make([]PeerRow, 0, len(envoys))
	for _, e := range envoys {
		if e.Revoked {
			continue
		}
		pe := rollup[e.ID]
		seeding := seedersByEnvoy[e.ID]
		row := PeerRow{
			Hostname:        e.Hostname,
			ID:              e.ID,
			LastSeen:        e.LastSeen,
			Online:          !e.Offline,
			SeedingCount:    seeding,
			SubstrateActive: pe != nil || seeding > 0,
		}
		if pe != nil {
			row.CachedCount = pe.cached
			row.DownloadingCount = pe.downloading
			row.FootprintBytes = pe.footprint
		}
		rows = append(rows, row)
	}

	sortPeerRows(rows, sortKey)
	out.Total = len(rows)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out.Envoys = rows
	out.Returned = len(rows)
	return out
}

func sortPeerRows(rows []PeerRow, key string) {
	switch key {
	case "hostname":
		sort.Slice(rows, func(i, j int) bool { return rows[i].Hostname < rows[j].Hostname })
	case "blobs":
		sort.Slice(rows, func(i, j int) bool {
			a, b := rows[i].CachedCount, rows[j].CachedCount
			if a == b {
				return rows[i].Hostname < rows[j].Hostname
			}
			return a > b
		})
	default:
		sort.Slice(rows, func(i, j int) bool {
			a, b := rows[i].FootprintBytes, rows[j].FootprintBytes
			if a == b {
				return rows[i].Hostname < rows[j].Hostname
			}
			return a > b
		})
	}
}
