// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/db"
)

// fleetCacheTTL controls how long cached fleet data is reused.
// Multiple HTMX fragments polling in the same cycle share one computation.
const fleetCacheTTL = 10 * time.Second

// FleetStatusEntry holds precomputed convergence status for a single envoy.
type FleetStatusEntry struct {
	ID           string
	Hostname     string
	IPAddress    string
	LastSeen     time.Time
	AgentVersion string
	Status       string  // convergence status: converged, relapsed, diverged, degraded, failed, no data
	FailurePct   float64 // 0-100: failed_count / module_count * 100 (from latest run)
	FailedCount  int64
	ModuleCount  int64
	LastRunID    string
	LastRunAt    time.Time
}

// FleetStatusCounts holds aggregate counts by convergence status.
type FleetStatusCounts struct {
	Total     int
	Converged int
	Relapsed  int
	Diverged  int
	Degraded  int
	Failed    int
	Offline   int
	NoData    int
}

// FleetCache caches precomputed fleet convergence status to avoid
// redundant ListEnvoys + N+1 queries across HTMX polling fragments.
type FleetCache struct {
	mu           sync.RWMutex
	queries      *db.Queries
	baseInterval time.Duration

	// Cached data.
	entries   []FleetStatusEntry
	counts    FleetStatusCounts
	updatedAt time.Time
}

// NewFleetCache creates a fleet cache backed by the given queries.
func NewFleetCache(queries *db.Queries, baseInterval time.Duration) *FleetCache {
	return &FleetCache{
		queries:      queries,
		baseInterval: baseInterval,
	}
}

// Counts returns cached fleet status counts, refreshing if stale.
func (fc *FleetCache) Counts() FleetStatusCounts {
	fc.refresh()
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.counts
}

// Entries returns all cached fleet status entries, refreshing if stale.
func (fc *FleetCache) Entries() []FleetStatusEntry {
	fc.refresh()
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	out := make([]FleetStatusEntry, len(fc.entries))
	copy(out, fc.entries)
	return out
}

// WorstEntries returns up to n entries sorted worst-status-first.
// Order: failed, diverged, relapsed, no data, converged.
func (fc *FleetCache) WorstEntries(n int) []FleetStatusEntry {
	all := fc.Entries()
	sortByWorstStatus(all)
	if n > 0 && len(all) > n {
		all = all[:n]
	}
	return all
}

// Invalidate forces the next call to recompute.
func (fc *FleetCache) Invalidate() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.updatedAt = time.Time{}
}

func (fc *FleetCache) refresh() {
	fc.mu.RLock()
	if time.Since(fc.updatedAt) < fleetCacheTTL {
		fc.mu.RUnlock()
		return
	}
	fc.mu.RUnlock()

	fc.mu.Lock()
	defer fc.mu.Unlock()

	// Double-check after acquiring write lock.
	if time.Since(fc.updatedAt) < fleetCacheTTL {
		return
	}

	ctx := context.Background()
	fleet, err := fc.queries.ListFleetConvergence(ctx)
	if err != nil {
		slog.Error("fleet cache: failed to load fleet convergence", "error", err)
		return
	}

	entries := make([]FleetStatusEntry, 0, len(fleet))
	var counts FleetStatusCounts

	offlineThreshold := 2 * fc.baseInterval

	for _, e := range fleet {
		runs := make([]convergence.RunInfo, 0, len(e.Runs))
		for _, r := range e.Runs {
			runs = append(runs, convergence.RunInfo{
				Status:       r.Status,
				ChangedCount: r.ChangedCount,
				FailedCount:  r.FailedCount,
				ModuleCount:  r.ModuleCount,
			})
		}

		status := convergence.ComputeConvergence(runs)

		var lastSeen time.Time
		if e.LastSeen.Valid {
			lastSeen = e.LastSeen.Time
		}

		offline := !lastSeen.IsZero() && time.Since(lastSeen) > offlineThreshold

		var lastRunID string
		var lastRunAt time.Time
		var failurePct float64
		var failedCount, moduleCount int64
		if len(e.Runs) > 0 {
			lastRunID = e.Runs[0].ID
			lastRunAt = e.Runs[0].StartedAt
			failedCount = e.Runs[0].FailedCount
			moduleCount = e.Runs[0].ModuleCount
			if moduleCount > 0 {
				failurePct = float64(failedCount) / float64(moduleCount) * 100
			}
		}

		entry := FleetStatusEntry{
			ID:           e.ID,
			Hostname:     e.Hostname,
			IPAddress:    e.IPAddress,
			LastSeen:     lastSeen,
			AgentVersion: e.AgentVersion,
			Status:       status,
			FailurePct:   failurePct,
			FailedCount:  failedCount,
			ModuleCount:  moduleCount,
			LastRunID:    lastRunID,
			LastRunAt:    lastRunAt,
		}
		entries = append(entries, entry)

		counts.Total++
		if offline {
			counts.Offline++
		}
		switch status {
		case "converged":
			counts.Converged++
		case "relapsed":
			counts.Relapsed++
		case "diverged":
			counts.Diverged++
		case "degraded":
			counts.Degraded++
		case "failed":
			counts.Failed++
		case "no data":
			counts.NoData++
		}
	}

	fc.entries = entries
	fc.counts = counts
	fc.updatedAt = time.Now()
}

// statusPriority returns a sort key: lower = worse.
func statusPriority(s string) int {
	switch s {
	case "failed":
		return 0
	case "degraded":
		return 1
	case "diverged":
		return 2
	case "relapsed":
		return 3
	case "no data":
		return 4
	case "converged":
		return 5
	default:
		return 6
	}
}

func sortByWorstStatus(entries []FleetStatusEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && statusPriority(entries[j].Status) < statusPriority(entries[j-1].Status); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}
