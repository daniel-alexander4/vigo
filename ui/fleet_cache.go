// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/config"
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
	// Offline iff lastSeen is older than 2*baseInterval. Stamped at refresh
	// time so paginated queries don't recompute per page-load.
	Offline bool
	// ObserveOnly iff the global checkin.observe_only flag is set OR the
	// matching envoys.vgo block has observe_only: true. Orthogonal to
	// convergence status (an observe-only envoy can be converged, relapsed,
	// or anything else — observe-only governs enforcement, not convergence).
	ObserveOnly bool
}

// FleetStatusCounts holds aggregate counts by convergence status.
// Observing is orthogonal to the convergence breakdown — an envoy in
// observe-only mode is also counted under its Status (Converged, Relapsed,
// etc.). Observing is the count of envoys with the observe-only flag set,
// reported alongside the convergence buckets, not summed into Total.
type FleetStatusCounts struct {
	Total     int
	Converged int
	Relapsed  int
	Diverged  int
	Degraded  int
	Failed    int
	Offline   int
	NoData    int
	Observing int
}

// FleetCache caches precomputed fleet convergence status to avoid
// redundant ListEnvoys + N+1 queries across HTMX polling fragments.
//
// configStore + globalObserveOnly let the cache stamp ObserveOnly into
// each FleetStatusEntry at refresh time, so the envoys page (and any
// other consumer) sees a self-consistent snapshot — convergence status
// + observe-only flag from the same point in time. Without this, the
// pills and the table would diverge on observe-only state the same way
// they previously diverged on relapsed/diverged convergence.
type FleetCache struct {
	mu                sync.RWMutex
	queries           *db.Queries
	configStore       *config.ConfigStore
	globalObserveOnly bool
	baseInterval      time.Duration

	// Cached data.
	entries   []FleetStatusEntry
	counts    FleetStatusCounts
	updatedAt time.Time
}

// NewFleetCache creates a fleet cache backed by the given queries.
//
// configStore is consulted at refresh time for per-envoy observe-only
// state via configStore.Match(hostname). globalObserveOnly comes from
// server.yaml's checkin.observe_only field — when true, every envoy
// is observe-only regardless of per-match config.
func NewFleetCache(queries *db.Queries, configStore *config.ConfigStore, globalObserveOnly bool, baseInterval time.Duration) *FleetCache {
	return &FleetCache{
		queries:           queries,
		configStore:       configStore,
		globalObserveOnly: globalObserveOnly,
		baseInterval:      baseInterval,
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

		// Per-envoy observe-only: global flag OR matching envoys.vgo
		// block. Orthogonal to convergence status — the cache reports
		// it as a flag on each entry rather than overriding Status,
		// so the envoys table can show real convergence next to an
		// "obs" badge.
		observeOnly := fc.globalObserveOnly
		if !observeOnly && fc.configStore != nil {
			if _, cfg := fc.configStore.Match(e.Hostname); cfg != nil {
				observeOnly = cfg.ObserveOnly
			}
		}

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
			Offline:      offline,
			ObserveOnly:  observeOnly,
		}
		entries = append(entries, entry)

		counts.Total++
		if offline {
			counts.Offline++
		}
		if observeOnly {
			counts.Observing++
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

// PaginatedEntries returns a hostname-sorted page of entries matching the
// given filter, plus a hasMore flag for cursor-based pagination. This is
// the source of truth for the envoys list table — the same entries that
// power FleetCache.Counts(), so the pills above the list and the list
// itself can never disagree.
//
// Filter semantics:
//   - statusFilter == "" — match all entries
//   - statusFilter == "offline" — match entries with Offline == true
//     (orthogonal to convergence status; an offline envoy keeps its
//     last-known convergence status)
//   - statusFilter == "observing" — match entries with ObserveOnly == true
//   - statusFilter == anything else — match entries whose Status equals it
//     exactly (converged, relapsed, diverged, degraded, failed, no data)
//
// search is a case-insensitive substring match on hostname. Empty matches
// everything.
//
// afterHostname is the cursor: return entries strictly after this hostname
// in the sort order. Empty string yields the first page.
//
// limit caps the returned slice. limit <= 0 disables the cap.
func (fc *FleetCache) PaginatedEntries(afterHostname string, limit int, statusFilter, search string) (entries []FleetStatusEntry, hasMore bool) {
	all := fc.Entries() // already hostname-sorted by ListFleetConvergence ORDER BY
	searchLower := strings.ToLower(search)

	filtered := make([]FleetStatusEntry, 0, len(all))
	for _, e := range all {
		if !matchesEnvoyFilter(e, statusFilter, searchLower) {
			continue
		}
		filtered = append(filtered, e)
	}

	start := 0
	if afterHostname != "" {
		for i, e := range filtered {
			if e.Hostname > afterHostname {
				start = i
				break
			}
			// If afterHostname is the last entry's hostname (or beyond),
			// start stays at 0 and we'll advance past the end below;
			// reset to len(filtered) so the slice is empty.
			if i == len(filtered)-1 {
				start = len(filtered)
			}
		}
	}

	end := len(filtered)
	if limit > 0 && start+limit < end {
		end = start + limit
		hasMore = true
	}
	if start > end {
		start = end
	}
	return filtered[start:end], hasMore
}

func matchesEnvoyFilter(e FleetStatusEntry, statusFilter, searchLower string) bool {
	switch statusFilter {
	case "":
		// no status filter
	case "offline":
		if !e.Offline {
			return false
		}
	case "observing":
		if !e.ObserveOnly {
			return false
		}
	default:
		if e.Status != statusFilter {
			return false
		}
	}
	if searchLower != "" {
		if !strings.Contains(strings.ToLower(e.Hostname), searchLower) {
			return false
		}
	}
	return true
}
