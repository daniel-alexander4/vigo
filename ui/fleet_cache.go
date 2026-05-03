// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"log/slog"
	"sort"
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

// FleetSource exposes pre-computed convergence counters from the live
// in-memory fleet index. When wired, FleetCache.Counts() returns these
// counts for the convergence and reachability buckets directly,
// eliminating the drift that previously existed between the dashboard
// pills (FleetCache, rebuilt every ~10s from the DB) and the metrics
// collector (which already consumed fleet.FleetIndex.ConvergenceSummary).
//
// Implemented in main.go via a tiny adapter around *fleet.FleetIndex.
// Observing is intentionally absent from the interface: it is per-envoy
// state derived by iterating FleetCache's enriched entry set, not a
// counter the FleetIndex tracks.
type FleetSource interface {
	ConvergenceSummary() FleetSummary
}

// FleetSummary mirrors the three-axis breakdown of
// fleet.ConvergenceCounters. Redefined in the ui package so FleetCache
// can take a FleetSource without importing server/fleet. Diverged ⊆
// Changed on the drift axis: an envoy in DriftDiverged is counted in
// both fields.
type FleetSummary struct {
	Total int
	// Failure axis (mutually exclusive). Sums to Total.
	Converged int
	Degraded  int
	Failed    int
	NoData    int
	// Drift axis (Diverged ⊆ Changed).
	Changed  int
	Diverged int
	// Reachability.
	Offline int
}

// FleetStatusEntry holds precomputed convergence status for a single envoy.
type FleetStatusEntry struct {
	ID           string
	Hostname     string
	IPAddress    string
	LastSeen     time.Time
	// LastSeenLabel is the human-readable rendering of LastSeen produced
	// by envoyFreshness — "just now" / "Ns ago" / "Nm ago" / etc. — and
	// is precomputed alongside Offline so the freshness color and the
	// freshness text can never disagree on the same row. Templates render
	// this string directly instead of calling timeAgo on LastSeen, since
	// timeAgo uses a fixed 30 s "just now" bucket that overlaps the
	// offline threshold at fast check-in intervals.
	LastSeenLabel string
	AgentVersion string
	Status       string                 // failure-axis status: converged, degraded, failed, no data
	Drift        convergence.DriftLevel // drift-axis bucket: none, changed, diverged
	FailurePct   float64                // 0-100: failed_count / module_count * 100 (from latest run)
	FailedCount  int64
	ModuleCount  int64
	ChangedCount int64                  // changed_count from the latest run; used to drive the drift signal
	LastRunID    string
	LastRunAt    time.Time
	// Offline iff lastSeen is older than freshness.StaleThreshold.
	// Stamped at refresh time so paginated queries don't recompute per
	// page-load. Derived by envoyFreshness in render.go, which routes
	// through freshness.IsStale — single source of truth shared with
	// LastSeenLabel above and with every server-side stale check.
	Offline bool
	// ObserveOnly iff the global checkin.observe_only flag is set OR the
	// matching envoys.vgo block has observe_only: true. Orthogonal to
	// convergence status (an observe-only envoy can be converged, relapsed,
	// or anything else — observe-only governs enforcement, not convergence).
	ObserveOnly bool
}

// FleetStatusCounts holds aggregate counts across three orthogonal
// axes plus observe-only. Failure-axis buckets (Converged, Degraded,
// Failed, NoData) are mutually exclusive and sum to Total. Drift-axis
// buckets (Changed, Diverged) overlap each other (Diverged ⊆ Changed)
// and are independent of the failure axis. Observing and Offline are
// each their own orthogonal flags counted alongside.
type FleetStatusCounts struct {
	Total int
	// Failure axis (sums to Total).
	Converged int
	Degraded  int
	Failed    int
	NoData    int
	// Drift axis (overlapping; Diverged ⊆ Changed).
	Changed  int
	Diverged int
	// Reachability + observability.
	Offline   int
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
	// fleetSource, when set, supplies the live in-memory convergence
	// counts so the dashboard pills do not drift from FleetIndex.
	// Wired via SetFleetSource at server startup. Observing still
	// comes from per-entry iteration of the cached entries.
	fleetSource FleetSource

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

// SetFleetSource wires the live convergence counter source. Pass an
// adapter around *fleet.FleetIndex at server startup so the dashboard
// pills consume the same in-memory counters as the metrics collector.
// Safe to call before or after the cache is populated.
func (fc *FleetCache) SetFleetSource(fs FleetSource) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.fleetSource = fs
}

// Counts returns cached fleet status counts, refreshing if stale. When
// a FleetSource is wired, the convergence and reachability buckets are
// served from live in-memory counters; Observing comes from per-entry
// iteration in the cache (FleetIndex does not track it).
func (fc *FleetCache) Counts() FleetStatusCounts {
	fc.refresh()
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	if fc.fleetSource != nil {
		s := fc.fleetSource.ConvergenceSummary()
		return FleetStatusCounts{
			Total:     s.Total,
			Converged: s.Converged,
			Degraded:  s.Degraded,
			Failed:    s.Failed,
			NoData:    s.NoData,
			Changed:   s.Changed,
			Diverged:  s.Diverged,
			Offline:   s.Offline,
			Observing: fc.counts.Observing,
		}
	}
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
		drift := convergence.ComputeDrift(runs)

		var lastSeen time.Time
		if e.LastSeen.Valid {
			lastSeen = e.LastSeen.Time
		}

		lastSeenLabel, offline := envoyFreshness(lastSeen, fc.baseInterval)

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
		var failedCount, moduleCount, changedCount int64
		if len(e.Runs) > 0 {
			lastRunID = e.Runs[0].ID
			lastRunAt = e.Runs[0].StartedAt
			failedCount = e.Runs[0].FailedCount
			moduleCount = e.Runs[0].ModuleCount
			changedCount = e.Runs[0].ChangedCount
			if moduleCount > 0 {
				failurePct = float64(failedCount) / float64(moduleCount) * 100
			}
		}

		entry := FleetStatusEntry{
			ID:            e.ID,
			Hostname:      e.Hostname,
			IPAddress:     e.IPAddress,
			LastSeen:      lastSeen,
			LastSeenLabel: lastSeenLabel,
			AgentVersion:  e.AgentVersion,
			Status:        status,
			Drift:         drift,
			FailurePct:    failurePct,
			FailedCount:   failedCount,
			ModuleCount:   moduleCount,
			ChangedCount:  changedCount,
			LastRunID:     lastRunID,
			LastRunAt:     lastRunAt,
			Offline:       offline,
			ObserveOnly:   observeOnly,
		}
		entries = append(entries, entry)

		counts.Total++
		if offline {
			counts.Offline++
		}
		if observeOnly {
			counts.Observing++
		}
		// Failure axis (mutually exclusive).
		switch convergence.ClassifyStatus(status) {
		case convergence.StatusConverged:
			counts.Converged++
		case convergence.StatusDegraded:
			counts.Degraded++
		case convergence.StatusFailed:
			counts.Failed++
		case convergence.StatusNoData:
			counts.NoData++
		}
		// Drift axis (overlapping). Diverged ⊆ Changed.
		if drift == convergence.DriftChanged || drift == convergence.DriftDiverged {
			counts.Changed++
		}
		if drift == convergence.DriftDiverged {
			counts.Diverged++
		}
	}

	fc.entries = entries
	fc.counts = counts
	fc.updatedAt = time.Now()
}

// statusPriority returns a sort key: lower = worse. Sorts on the
// failure axis only — drift is reflected in the dedicated drift pills,
// not in row sort order. Diverged envoys with zero failures still sort
// near the bottom because they're functionally healthy from the
// "is anything broken right now" perspective.
func statusPriority(s string) int {
	switch convergence.ClassifyStatus(s) {
	case convergence.StatusFailed:
		return 0
	case convergence.StatusDegraded:
		return 1
	case convergence.StatusNoData:
		return 2
	case convergence.StatusConverged:
		return 3
	default:
		return 4
	}
}

func sortByWorstStatus(entries []FleetStatusEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return worstStatusLess(entries[i], entries[j])
	})
}

// worstStatusLess orders entries by triage priority: worst convergence
// status first, then highest failure percentage, then hostname for stable
// tiebreaking. Used both by WorstEntries (dashboard) and by the envoys
// list when the operator picks "Worst first" — the same comparator
// keeps the two surfaces consistent.
func worstStatusLess(a, b FleetStatusEntry) bool {
	pa, pb := statusPriority(a.Status), statusPriority(b.Status)
	if pa != pb {
		return pa < pb
	}
	if a.FailurePct != b.FailurePct {
		return a.FailurePct > b.FailurePct
	}
	return a.Hostname < b.Hostname
}

// sortFleetEntries reorders entries in place per the operator's sort
// choice. The "status" column means triage-first when ascending — that
// is the default landing experience for /envoys at scale, where the
// alphabetically-first envoy is rarely the one the operator came to fix.
// "last_run" defaults to newest-first when descending; "hostname" /
// "ip" / "last_seen" follow the obvious lexicographic / temporal order.
func sortFleetEntries(entries []FleetStatusEntry, col, dir string) {
	switch col {
	case "", "status":
		sortByWorstStatus(entries)
		if dir == "desc" {
			reverseEntries(entries)
		}
	case "hostname":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Hostname < entries[j].Hostname })
		if dir == "desc" {
			reverseEntries(entries)
		}
	case "ip":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].IPAddress < entries[j].IPAddress })
		if dir == "desc" {
			reverseEntries(entries)
		}
	case "last_seen":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].LastSeen.Before(entries[j].LastSeen) })
		if dir != "asc" {
			reverseEntries(entries)
		}
	case "last_run":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].LastRunAt.Before(entries[j].LastRunAt) })
		if dir != "asc" {
			reverseEntries(entries)
		}
	default:
		sortByWorstStatus(entries)
	}
}

func reverseEntries(entries []FleetStatusEntry) {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
}

// PaginatedEntries returns a sorted, filtered, offset-paginated page of
// entries matching the given filters and sort, the total number of
// matching entries (for "1000 of 12,847" footers), and a hasMore flag
// for the "load more" trigger. Source of truth for the envoys list
// table — the same entries that power FleetCache.Counts(), so the pills
// above the list and the list itself can never disagree.
//
// Filter semantics (combined with AND across axes; empty = match all):
//   - convFilter: "" / "converged" / "degraded" / "failed" / "no data"
//   - driftFilter: "" / "changed" / "diverged" (Diverged is a subset of
//     Changed; clicking Changed includes Diverged envoys)
//   - reachFilter: "" / "offline" / "observing"
//
// search is a case-insensitive substring match on hostname.
//
// sortCol + sortDir control the order. Empty sortCol means triage-first
// (failure-axis worst, highest failure%, hostname tiebreak).
//
// offset is the row index to start at after sorting + filtering. limit
// caps the returned slice. limit <= 0 disables the cap.
func (fc *FleetCache) PaginatedEntries(offset, limit int, convFilter, driftFilter, reachFilter, search, sortCol, sortDir string) (entries []FleetStatusEntry, total int, hasMore bool) {
	all := fc.Entries()
	searchLower := strings.ToLower(search)

	filtered := make([]FleetStatusEntry, 0, len(all))
	for _, e := range all {
		if !matchesEnvoyFilter(e, convFilter, driftFilter, reachFilter, searchLower) {
			continue
		}
		filtered = append(filtered, e)
	}

	sortFleetEntries(filtered, sortCol, sortDir)

	total = len(filtered)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return nil, total, false
	}
	end := total
	if limit > 0 && offset+limit < total {
		end = offset + limit
		hasMore = true
	}
	return filtered[offset:end], total, hasMore
}

func matchesEnvoyFilter(e FleetStatusEntry, convFilter, driftFilter, reachFilter, searchLower string) bool {
	if convFilter != "" && e.Status != convFilter {
		return false
	}
	switch driftFilter {
	case "":
		// no drift filter
	case "changed":
		// Diverged ⊆ Changed; both buckets satisfy the Changed filter.
		if e.Drift != convergence.DriftChanged && e.Drift != convergence.DriftDiverged {
			return false
		}
	case "diverged":
		if e.Drift != convergence.DriftDiverged {
			return false
		}
	default:
		// unknown drift filter — fall through, match nothing safely
		return false
	}
	switch reachFilter {
	case "":
		// no reach filter
	case "offline":
		if !e.Offline {
			return false
		}
	case "observing":
		if !e.ObserveOnly {
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
