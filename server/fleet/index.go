// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package fleet provides an in-memory FleetIndex that holds the current state
// of every envoy. All real-time queries (pubkey lookups, check-in state reads,
// targeting, convergence counters) are served from this index. SQLite remains
// the durable store, fed asynchronously via the StateFlusher.
package fleet

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.alexander4.io/vigo/server/convergence"
	voied25519 "github.com/oasisprotocol/curve25519-voi/primitives/ed25519"
)

// MaxRecentRuns bounds the per-envoy trailing run slice held in
// [EnvoyState.RecentRuns]. [convergence.ComputeConvergence] only
// looks at the first DivergedThreshold entries, so storing a few
// extra past that is wasteful. Ten is comfortably above the
// threshold and leaves room for future drift-detection windows.
const MaxRecentRuns = 10

// envoyStripes is the number of stripes in FleetIndex.envoyLocks. Power
// of two so id-hash → stripe is a bit mask. 256 stripes spreads per-envoy
// write contention thinly even at 25K-envoy scale (≈100 envoys per
// stripe, ≈0.1% stripe utilization at the current per-checkin cost).
const envoyStripes = 256

// EnvoyState holds the in-memory state of a single envoy. Per-envoy field
// writes serialize through FleetIndex.lockFor(id) — a stripe lock taken by
// the hot-path setters (MarkSeen, MarkCycleDuration, SetIP, SetAgentVersion,
// and the rare-transition slow path of SetOffline) under f.mu.RLock so
// concurrent CheckIns for different envoys do not serialize through the
// fleet-wide write lock. Readers that copy more than one field must acquire
// the same stripe lock for snapshot consistency.
type EnvoyState struct {
	ID                string
	Hostname          string
	IP                string
	PubkeyPEM         string
	EnrolledAt        time.Time
	LastSeen          time.Time
	Revoked           bool
	ConvergenceStatus string
	// Drift is the drift-axis bucket for this envoy — orthogonal to
	// ConvergenceStatus. None / Changed / Diverged. Recomputed on every
	// UpdateRunStatus from the trailing run history. See server/convergence.
	Drift               convergence.DriftLevel
	LastRunStatus       string
	LastRunID           string
	LastRunChangedCount int64
	LastRunFailedCount  int64
	LastRunModuleCount  int64
	LastRunAt           time.Time
	// RecentRuns holds the most-recent-first trailing run history used to
	// compute ConvergenceStatus. Bounded to [MaxRecentRuns] entries so
	// drift detection (changed/diverged) has enough signal without
	// growing unbounded. Populated by [FleetIndex.UpdateRunStatus] at
	// check-in time and rehydrated from the DB on server startup.
	RecentRuns            []convergence.RunInfo
	Traits                map[string]string // flattened key→value for trait index
	PendingRotatedSecrets []string
	ForcePush             bool
	ForceUpdate           bool      // ephemeral: agent should download latest binary and restart
	AgentVersion          string    // agent binary version reported on check-in
	StreamRequested       bool      // ephemeral: server wants agent to open stream (not persisted to DB)
	TunnelRequested       bool      // ephemeral: server wants agent to open TunnelStream (Scrier)
	FastPoll              bool      // ephemeral: next check-in response uses 1s interval (cleared after delivery)
	RunForced             bool      // ephemeral: the run the agent will next report was force-triggered; consumed by ConsumeRunTrigger at run-create. Not persisted.
	Gateway               string    // hostname of gateway envoy (empty for normal envoys)
	DeviceType            string    // network device type (empty for normal envoys)
	Tags                  []string  // user-assigned tags for targeting
	MaintenanceUntil      time.Time // zero = not in maintenance
	ObserveOnly           bool      // observe-only mode: report drift only
	Offline               bool      // true when envoy hasn't checked in within 2×base interval
	// recentCheckinDeltasMs is a 3-slot ring of agent-reported
	// inter-CheckIn deltas (ms). Smoothing prevents a single bouncy
	// cycle from flapping the staleness threshold by orders of
	// magnitude. Zero entries are skipped by the EnvoyCycle resolver.
	recentCheckinDeltasMs  [3]uint32
	recentCheckinDeltasIdx int8 // next slot to write
}

// ConvergenceCounters holds pre-computed totals across three axes.
// Each axis is independent — an envoy is counted in exactly one
// failure bucket, in zero or one drift bucket (Diverged is a strict
// subset of Changed; an envoy in Diverged is counted in both), and
// in Offline if reachability has lapsed. Total counts every envoy
// once.
type ConvergenceCounters struct {
	Total int

	// Failure axis (mutually exclusive). Sums to Total.
	Converged int
	Degraded  int
	Failed    int
	NoData    int

	// Drift axis (orthogonal to failure). Diverged ⊆ Changed: an envoy
	// with 5+ consecutive runs of changes counts toward both.
	Changed  int
	Diverged int

	// Reachability axis (orthogonal to both).
	Offline int
}

// RunSummary is a brief record of a completed run for the recent-runs ring buffer.
type RunSummary struct {
	RunID        string
	EnvoyID      string
	Hostname     string
	Status       string
	Trigger      string // "scheduled" | "force-push"
	ChangedCount int64
	FailedCount  int64
	FinishedAt   time.Time
}

// ConvergenceSnapshot is a daily convergence snapshot for the sparkline.
type ConvergenceSnapshot struct {
	Timestamp time.Time
	Counters  ConvergenceCounters
}

// FleetIndex is a concurrency-safe in-memory index of all envoys. It provides
// O(1) lookups by ID, hostname, and pubkey, O(k) queries by status and trait,
// and pre-computed convergence counters. Write methods mark envoys dirty for
// async flush to SQLite.
type FleetIndex struct {
	mu         sync.RWMutex
	envoys     map[string]*EnvoyState       // ID → state
	byHostname map[string]string            // hostname → ID
	pubkeys    map[string]ed25519.PublicKey // ID → parsed key (raw bytes, for export via /api/v1/fleet/pubkeys)
	// expandedPubkeys caches the curve25519-voi pre-expanded form keyed by
	// envoy ID. Used by the hot-path Verify method below to skip the
	// curve-point decompression that stdlib ed25519.Verify does on every
	// call — ed25519 verify was ~26 % of vigosrv CPU at 8.7K req/s before
	// this cache, dominated by VarTimeDoubleScalarBaseMult. ~1.47 KiB per
	// envoy (≈ 73 MiB at 50K). nil entry on expansion failure → Verify
	// returns found=false and the caller falls through to the slow path.
	expandedPubkeys    map[string]*voied25519.ExpandedPublicKey
	byStatus           map[string]map[string]bool            // status → set of IDs
	byTrait            map[string]map[string]map[string]bool // trait_key → trait_value → set of IDs
	byTag              map[string]map[string]bool            // tag → set of IDs
	convergence        ConvergenceCounters
	recentRuns         *ringBuffer
	convergenceHistory *convergenceRing // rolling 30-day daily snapshots

	// byTraitMu / byStatusMu / recentRunsMu split out the reverse indexes and
	// the recent-runs ring from f.mu. Each is mutated on its own hot path
	// (UpdateTraits, UpdateRunStatus, AppendRecentRun) and at fleet scale
	// holding f.mu.Lock for those mutations queued every concurrent CheckIn's
	// f.mu.RLock. Splitting the lock domains made each path independent.
	//
	// Lock ordering when more than one is needed: f.mu → byTraitMu →
	// byStatusMu → recentRunsMu. AddEnvoy / RemoveEnvoy / SetRevoked acquire
	// f.mu plus byTraitMu (and byStatusMu when applicable); the hot-path
	// updaters acquire only their own domain lock, optionally preceded by a
	// brief f.mu.RLock for the envoy-id → state lookup.
	byTraitMu    sync.RWMutex
	byStatusMu   sync.RWMutex
	recentRunsMu sync.Mutex

	// Convergence counters split out of the ConvergenceCounters struct because
	// they're mutated on hot paths — Offline on CheckIn (MarkSeen / SetOffline),
	// the rest on ReportResult (UpdateRunStatus). The pre-0.68.x design kept
	// them under f.mu.Lock and turned a single ReportResult into a global write
	// lock that blocked every concurrent CheckIn's f.mu.RLock. Read paths
	// (ConvergenceSummary / SnapshotConvergence) stitch the atomic values into
	// the snapshot struct at read time so external callers don't see a
	// behavior change. Total stays in the struct under f.mu — it only changes
	// on enrollment / revocation / removal, never on the run-reporting path.
	convergenceOffline   atomic.Int64
	convergenceConverged atomic.Int64
	convergenceDegraded  atomic.Int64
	convergenceFailed    atomic.Int64
	convergenceNoData    atomic.Int64
	convergenceChanged   atomic.Int64
	convergenceDiverged  atomic.Int64

	// dirty holds the set of envoy IDs with pending state changes for the
	// next flusher tick. A sync.Map (not f.mu-protected map) so hot-path
	// setters can mark dirty under f.mu.RLock + stripe lock without
	// blocking on concurrent CheckIns for other envoys.
	dirty sync.Map // string id -> struct{}{}

	// envoyLocks stripes per-envoy field writes across 256 mutexes. Two
	// envoys whose IDs hash to the same stripe serialize on the same lock,
	// but with 256 stripes false-sharing is negligible at any realistic
	// fleet size, and the structure stays trivial (no per-envoy
	// allocation, no growth as enrollment scales).
	envoyLocks [envoyStripes]sync.Mutex

	// Wake channels — separate mutex so blocking Wake RPCs don't hold the main lock.
	wakeMu    sync.Mutex
	wakeChans map[string]chan struct{} // envoy_id → signal channel
}

// lockFor returns the stripe mutex that guards per-envoy field writes for
// the given envoy ID. Hash-and-mask is O(len(id)) but the IDs are short
// fixed-length UUIDs, so this is sub-100ns per call.
func (f *FleetIndex) lockFor(id string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(id))
	return &f.envoyLocks[h.Sum32()&(envoyStripes-1)]
}

// NewFleetIndex creates an empty FleetIndex.
func NewFleetIndex() *FleetIndex {
	return &FleetIndex{
		envoys:             make(map[string]*EnvoyState),
		byHostname:         make(map[string]string),
		pubkeys:            make(map[string]ed25519.PublicKey),
		expandedPubkeys:    make(map[string]*voied25519.ExpandedPublicKey),
		byStatus:           make(map[string]map[string]bool),
		byTrait:            make(map[string]map[string]map[string]bool),
		byTag:              make(map[string]map[string]bool),
		recentRuns:         newRingBuffer(5000),
		convergenceHistory: newConvergenceRing(30),
		wakeChans:          make(map[string]chan struct{}),
	}
}

// --- Read methods (acquire RLock) ---

// ActiveCount returns the number of non-revoked envoys currently tracked.
// Backed by the pre-computed convergence.Total counter, so this is O(1) and
// safe to call from the enrollment hot path. Used by the license enforcement
// gate to check the fleet against the node limit before inserting a new envoy.
func (f *FleetIndex) ActiveCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.convergence.Total
}

// GetEnvoy returns a copy of the EnvoyState for the given ID.
// Returns nil if not found.
func (f *FleetIndex) GetEnvoy(id string) *EnvoyState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ts := f.envoys[id]
	if ts == nil {
		return nil
	}
	lk := f.lockFor(id)
	lk.Lock()
	cp := *ts
	if ts.PendingRotatedSecrets != nil {
		cp.PendingRotatedSecrets = make([]string, len(ts.PendingRotatedSecrets))
		copy(cp.PendingRotatedSecrets, ts.PendingRotatedSecrets)
	}
	if ts.Traits != nil {
		cp.Traits = make(map[string]string, len(ts.Traits))
		for k, v := range ts.Traits {
			cp.Traits[k] = v
		}
	}
	lk.Unlock()
	return &cp
}

// IsActive returns true when the envoy ID exists and is not revoked. Both
// the deleted case (no row in the index) and the revoked case (row present,
// Revoked flag set) report as inactive. Used by swarm aggregators that need
// to drop revoked / deleted envoys from operator-facing surfaces — see
// `server/swarm/tracker.go::SetActiveChecker` for the canonical consumer.
func (f *FleetIndex) IsActive(id string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	src := f.envoys[id]
	return src != nil && !src.Revoked
}

// GetEnvoyLite returns a shallow copy of the EnvoyState without deep-copying
// Traits or PendingRotatedSecrets. It also returns existence and revoked status,
// collapsing IsRevoked + GetEnvoy into one lock acquisition. The caller must
// not mutate the Traits or PendingRotatedSecrets slices on the returned value.
func (f *FleetIndex) GetEnvoyLite(id string) (ts *EnvoyState, exists, revoked bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	src := f.envoys[id]
	if src == nil {
		return nil, false, false
	}
	lk := f.lockFor(id)
	lk.Lock()
	cp := *src // shallow copy — shares Traits/PendingRotatedSecrets slices
	revoked = src.Revoked
	lk.Unlock()
	return &cp, true, revoked
}

// GetPubKey returns the parsed ED25519 public key for the given envoy ID
// as raw 32-byte ed25519.PublicKey. Returns nil if not found. Hot-path
// verification should call Verify instead — this accessor exists for
// callers that need the raw bytes (e.g. /api/v1/fleet/pubkeys export).
func (f *FleetIndex) GetPubKey(id string) ed25519.PublicKey {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.pubkeys[id]
}

// Verify checks sig against msg using the cached expanded public key for
// envoyID. Returns (verified, found). When found is false the caller
// should fall through to the slow path (DB lookup + parse + verify) —
// this happens for envoys not yet in the index (admitted during a
// rehydration race) or with malformed stored keys that couldn't expand.
//
// Uses curve25519-voi's VerifyExpanded which skips the per-call curve-
// point decompression stdlib ed25519.Verify does on every call.
func (f *FleetIndex) Verify(envoyID string, msg, sig []byte) (verified, found bool) {
	f.mu.RLock()
	pub := f.expandedPubkeys[envoyID]
	f.mu.RUnlock()
	if pub == nil {
		return false, false
	}
	return voied25519.VerifyExpanded(pub, msg, sig), true
}

// GetByHostname returns the envoy ID for the given hostname.
// Returns "" if not found.
func (f *FleetIndex) GetByHostname(hostname string) string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.byHostname[hostname]
}

// ListByGlob returns all non-revoked envoys whose hostname matches the glob pattern.
func (f *FleetIndex) ListByGlob(pattern string) []TargetInfo {
	f.mu.RLock()
	defer f.mu.RUnlock()
	// Pre-allocate for ~10% match rate to avoid repeated slice growth.
	estimate := len(f.envoys) / 10
	if estimate < 8 {
		estimate = 8
	}
	results := make([]TargetInfo, 0, estimate)
	for _, ts := range f.envoys {
		if ts.Revoked {
			continue
		}
		if matched, _ := filepath.Match(pattern, ts.Hostname); matched {
			results = append(results, TargetInfo{EnvoyID: ts.ID, Hostname: ts.Hostname})
		}
	}
	return results
}

// ListByTrait returns all non-revoked envoys whose flattened traits contain key=value.
// Runs in two sequential phases — byTraitMu to collect candidate IDs, then f.mu
// to resolve each ID to a TargetInfo — so the byTrait-protecting lock is held
// only across the cheap map lookup. Slight staleness between phases (an envoy
// could be removed between collect and resolve) is acceptable: the f.mu phase
// filters out IDs no longer in the index.
func (f *FleetIndex) ListByTrait(key, value string) []TargetInfo {
	f.byTraitMu.RLock()
	idSet := f.byTrait[key][value]
	candidates := make([]string, 0, len(idSet))
	for id := range idSet {
		candidates = append(candidates, id)
	}
	f.byTraitMu.RUnlock()
	if len(candidates) == 0 {
		return nil
	}

	f.mu.RLock()
	defer f.mu.RUnlock()
	var results []TargetInfo
	for _, id := range candidates {
		ts := f.envoys[id]
		if ts != nil && !ts.Revoked {
			results = append(results, TargetInfo{EnvoyID: ts.ID, Hostname: ts.Hostname})
		}
	}
	return results
}

// TargetInfo is the minimal info returned by targeting queries.
type TargetInfo struct {
	EnvoyID  string
	Hostname string
}

// ConvergenceSummary returns the pre-computed convergence counters.
// Every per-axis counter except Total is loaded from its atomic counterpart
// and stitched into the snapshot — the ConvergenceCounters struct is the
// public contract; the atomics are internal. Total is the only field still
// in the struct (changes only on enrollment / revocation / removal, never
// on the run-reporting hot path).
func (f *FleetIndex) ConvergenceSummary() ConvergenceCounters {
	f.mu.RLock()
	c := f.convergence
	f.mu.RUnlock()
	c.Offline = int(f.convergenceOffline.Load())
	c.Converged = int(f.convergenceConverged.Load())
	c.Degraded = int(f.convergenceDegraded.Load())
	c.Failed = int(f.convergenceFailed.Load())
	c.NoData = int(f.convergenceNoData.Load())
	c.Changed = int(f.convergenceChanged.Load())
	c.Diverged = int(f.convergenceDiverged.Load())
	return c
}

// ConvergenceCounts returns the convergence breakdown as flat values
// across the failure axis plus the drift axis plus reachability.
// Satisfies the spanner.ConvergenceSource interface.
func (f *FleetIndex) ConvergenceCounts() (total, converged, degraded, failed, noData, changed, diverged, offline int) {
	c := f.ConvergenceSummary()
	return c.Total, c.Converged, c.Degraded, c.Failed, c.NoData, c.Changed, c.Diverged, c.Offline
}

// SnapshotConvergence records a convergence snapshot for the sparkline.
func (f *FleetIndex) SnapshotConvergence() {
	f.mu.RLock()
	c := f.convergence
	c.Offline = int(f.convergenceOffline.Load())
	c.Converged = int(f.convergenceConverged.Load())
	c.Degraded = int(f.convergenceDegraded.Load())
	c.Failed = int(f.convergenceFailed.Load())
	c.NoData = int(f.convergenceNoData.Load())
	c.Changed = int(f.convergenceChanged.Load())
	c.Diverged = int(f.convergenceDiverged.Load())
	snap := ConvergenceSnapshot{
		Timestamp: time.Now(),
		Counters:  c,
	}
	f.mu.RUnlock()

	f.mu.Lock()
	f.convergenceHistory.Push(snap)
	f.mu.Unlock()
}

// ConvergenceSparkline returns the rolling convergence history snapshots.
func (f *FleetIndex) ConvergenceSparkline() []ConvergenceSnapshot {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.convergenceHistory.All()
}

// AllEnvoys returns a copy of all EnvoyState values.
func (f *FleetIndex) AllEnvoys() []EnvoyState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	result := make([]EnvoyState, 0, len(f.envoys))
	for id, ts := range f.envoys {
		lk := f.lockFor(id)
		lk.Lock()
		result = append(result, *ts)
		lk.Unlock()
	}
	return result
}

// PaginatedEnvoys returns up to `limit` non-revoked envoys, sorted by hostname,
// starting after `afterHostname` (exclusive). Pass "" to start from the beginning.
// Returns the envoys and whether there are more results.
func (f *FleetIndex) PaginatedEnvoys(afterHostname string, limit int, statusFilter string, search string) ([]EnvoyState, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// Collect matching envoys.
	searchLower := strings.ToLower(search)
	var candidates []EnvoyState
	for id, ts := range f.envoys {
		if ts.Revoked {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(ts.Hostname), searchLower) {
			continue
		}
		if statusFilter != "" && statusFilter != "offline" && ts.ConvergenceStatus != statusFilter {
			continue
		}
		if afterHostname != "" && ts.Hostname <= afterHostname {
			continue
		}
		lk := f.lockFor(id)
		lk.Lock()
		if statusFilter == "offline" && !ts.Offline {
			lk.Unlock()
			continue
		}
		candidates = append(candidates, *ts)
		lk.Unlock()
	}

	// Sort by hostname.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Hostname < candidates[j].Hostname
	})

	hasMore := len(candidates) > limit
	if hasMore {
		candidates = candidates[:limit]
	}
	return candidates, hasMore
}

// SnapshotEnvoys returns a copy of every non-revoked envoy's state under a
// single read lock. Each EnvoyState already carries the canonical
// convergence classification (ConvergenceStatus, Drift) computed at
// run-ingest — callers read it rather than re-deriving from run history.
func (f *FleetIndex) SnapshotEnvoys() []EnvoyState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]EnvoyState, 0, len(f.envoys))
	for id, ts := range f.envoys {
		if ts.Revoked {
			continue
		}
		lk := f.lockFor(id)
		lk.Lock()
		out = append(out, *ts)
		lk.Unlock()
	}
	return out
}

// RecentRuns returns the last n run summaries (most recent first).
func (f *FleetIndex) RecentRuns(n int) []RunSummary {
	f.recentRunsMu.Lock()
	defer f.recentRunsMu.Unlock()
	return f.recentRuns.Last(n)
}

// ListByTag returns all non-revoked envoys with the given tag.
func (f *FleetIndex) ListByTag(tag string) []TargetInfo {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ids, ok := f.byTag[tag]
	if !ok {
		return nil
	}
	var results []TargetInfo
	for id := range ids {
		ts := f.envoys[id]
		if ts != nil && !ts.Revoked {
			results = append(results, TargetInfo{EnvoyID: ts.ID, Hostname: ts.Hostname})
		}
	}
	return results
}

// IsInMaintenance returns true if the envoy is currently in a maintenance window.
func (f *FleetIndex) IsInMaintenance(id string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ts := f.envoys[id]
	if ts == nil {
		return false
	}
	return !ts.MaintenanceUntil.IsZero() && time.Now().Before(ts.MaintenanceUntil)
}

// ConvergencePercent returns the percentage of non-revoked envoys that have converged.
func (f *FleetIndex) ConvergencePercent() float64 {
	f.mu.RLock()
	total := f.convergence.Total
	f.mu.RUnlock()
	if total == 0 {
		return 100.0
	}
	return float64(f.convergenceConverged.Load()) / float64(total) * 100.0
}

// --- Write methods (acquire Lock) ---

// AddEnvoy inserts a new envoy into the index. If a envoy with the same ID
// already exists, it is overwritten.
func (f *FleetIndex) AddEnvoy(ts EnvoyState, pubKey ed25519.PublicKey) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Capture the drift level the prior entry contributed to the fleet
	// counters (DriftNone if no prior entry or it was revoked — revoked
	// envoys don't contribute). Reconciled with the new contribution in a
	// single bumpDriftCounters call below so initialization and
	// re-enrollment go through the same transition arithmetic
	// UpdateRunStatus uses.
	oldDrift := convergence.DriftNone

	// byTrait + byStatus operations need their dedicated mutexes (separate
	// from f.mu since 0.68.6 / 0.68.8 follow-ups). Acquired inside the
	// f.mu critical section per the documented lock ordering: f.mu →
	// byTraitMu → byStatusMu.
	f.byTraitMu.Lock()
	defer f.byTraitMu.Unlock()
	f.byStatusMu.Lock()
	defer f.byStatusMu.Unlock()

	// Remove old hostname mapping if it changed.
	if old, ok := f.envoys[ts.ID]; ok {
		if old.Hostname != ts.Hostname {
			delete(f.byHostname, old.Hostname)
		}
		f.removeFromStatus(ts.ID, old.ConvergenceStatus)
		f.removeFromTraits(ts.ID, old.Traits)
		f.removeFromTags(ts.ID, old.Tags)
		if !old.Revoked {
			f.convergence.Total--
			f.decrementCounter(old.ConvergenceStatus)
			if old.Offline {
				f.convergenceOffline.Add(-1)
			}
			oldDrift = old.Drift
		}
	}

	state := ts // copy
	f.envoys[ts.ID] = &state
	f.byHostname[ts.Hostname] = ts.ID
	if pubKey != nil {
		f.pubkeys[ts.ID] = pubKey
		// Pre-expand the curve point once; verify hot path reuses it.
		// Failure (malformed key) is logged but non-fatal — Verify returns
		// found=false and the interceptor's slow path takes over.
		if exp, err := voied25519.NewExpandedPublicKey(voied25519.PublicKey(pubKey)); err == nil {
			f.expandedPubkeys[ts.ID] = exp
		} else {
			slog.Warn("fleet: failed to expand envoy pubkey for fast-verify cache; slow-path will be used", "envoy_id", ts.ID, "error", err)
			delete(f.expandedPubkeys, ts.ID)
		}
	}

	newDrift := convergence.DriftNone
	if !ts.Revoked {
		f.addToStatus(ts.ID, ts.ConvergenceStatus)
		f.addToTraits(ts.ID, ts.Traits)
		f.addToTags(ts.ID, ts.Tags)
		f.convergence.Total++
		f.incrementCounter(ts.ConvergenceStatus)
		if ts.Offline {
			f.convergenceOffline.Add(1)
		}
		newDrift = ts.Drift
	}
	f.bumpDriftCounters(oldDrift, newDrift)
}

// RemoveEnvoy removes a envoy from the index entirely.
func (f *FleetIndex) RemoveEnvoy(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Lock-ordering: f.mu → byTraitMu → byStatusMu (see field comment).
	f.byTraitMu.Lock()
	defer f.byTraitMu.Unlock()
	f.byStatusMu.Lock()
	defer f.byStatusMu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}

	delete(f.byHostname, ts.Hostname)
	delete(f.pubkeys, id)
	delete(f.expandedPubkeys, id)
	f.removeFromStatus(id, ts.ConvergenceStatus)
	f.removeFromTraits(id, ts.Traits)
	f.removeFromTags(id, ts.Tags)
	if !ts.Revoked {
		f.convergence.Total--
		f.decrementCounter(ts.ConvergenceStatus)
		if ts.Offline {
			f.convergenceOffline.Add(-1)
		}
		f.bumpDriftCounters(ts.Drift, convergence.DriftNone)
	}
	delete(f.envoys, id)
	f.dirty.Delete(id)
}

// SetOffline updates an envoy's offline state without changing its convergence status.
// Uses the same RLock + stripe pattern as the hot-path setters so it doesn't race
// with concurrent MarkSeen / MarkSeenInMemory writes on ts.Offline. The counter
// update is a lock-free atomic; the stripe lock guarantees at most one observer
// of the transition per envoy.
func (f *FleetIndex) SetOffline(id string, offline bool) {
	f.mu.RLock()
	ts := f.envoys[id]
	f.mu.RUnlock()
	if ts == nil {
		return
	}

	lk := f.lockFor(id)
	lk.Lock()
	if ts.Revoked || ts.Offline == offline {
		lk.Unlock()
		return
	}
	ts.Offline = offline
	lk.Unlock()

	if offline {
		f.convergenceOffline.Add(1)
	} else {
		f.convergenceOffline.Add(-1)
	}
}

// SetRevoked marks a envoy as revoked.
func (f *FleetIndex) SetRevoked(id string, revoked bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Lock-ordering: f.mu → byStatusMu (only one acquired beyond f.mu here).
	f.byStatusMu.Lock()
	defer f.byStatusMu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}

	wasRevoked := ts.Revoked
	ts.Revoked = revoked

	if !wasRevoked && revoked {
		// Becoming revoked: remove from active counters.
		f.removeFromStatus(id, ts.ConvergenceStatus)
		f.convergence.Total--
		f.decrementCounter(ts.ConvergenceStatus)
		f.bumpDriftCounters(ts.Drift, convergence.DriftNone)
	} else if wasRevoked && !revoked {
		// Becoming active again: add to counters.
		f.addToStatus(id, ts.ConvergenceStatus)
		f.convergence.Total++
		f.incrementCounter(ts.ConvergenceStatus)
		f.bumpDriftCounters(convergence.DriftNone, ts.Drift)
	}
}

// MarkSeen updates the LastSeen timestamp for a envoy and marks it dirty
// for async flush to SQLite. Also clears the offline flag if set.
//
// Hot path: takes f.mu.RLock for the map lookup, then the per-envoy stripe
// lock for the field write. Concurrent CheckIns for different envoys do not
// queue on any shared write lock. The Offline-transition counter update is
// a lock-free atomic decrement, fired only when an envoy actually comes back
// online (per-envoy mutex ensures only the first observer of the transition
// counts it).
func (f *FleetIndex) MarkSeen(id string, t time.Time) {
	f.mu.RLock()
	ts := f.envoys[id]
	f.mu.RUnlock()
	if ts == nil {
		return
	}

	lk := f.lockFor(id)
	lk.Lock()
	ts.LastSeen = t
	wasOffline := ts.Offline
	ts.Offline = false
	lk.Unlock()

	f.dirty.Store(id, struct{}{})

	if wasOffline {
		f.convergenceOffline.Add(-1)
	}
}

// MarkCycleDuration records an agent-reported inter-CheckIn delta in
// the rolling 3-slot ring. Zero values are silently ignored
// (bootstrap: agent has no prior CheckIn yet). Values are clamped to
// freshness.MaxReportedCycle by the caller before reaching here.
// In-memory only — cycle data is rebuilt within one CheckIn per envoy
// after a server restart.
func (f *FleetIndex) MarkCycleDuration(id string, deltaMs uint32) {
	if deltaMs == 0 {
		return
	}
	f.mu.RLock()
	ts := f.envoys[id]
	f.mu.RUnlock()
	if ts == nil {
		return
	}

	lk := f.lockFor(id)
	lk.Lock()
	defer lk.Unlock()
	idx := ts.recentCheckinDeltasIdx
	if idx < 0 || idx >= int8(len(ts.recentCheckinDeltasMs)) {
		idx = 0
	}
	ts.recentCheckinDeltasMs[idx] = deltaMs
	idx++
	if int(idx) >= len(ts.recentCheckinDeltasMs) {
		idx = 0
	}
	ts.recentCheckinDeltasIdx = idx
}

// EnvoyCycle returns the smoothed observed inter-CheckIn cadence for
// one envoy — the max across the rolling 3-slot ring of agent-reported
// deltas. Returns 0 when no data has been recorded yet (bootstrap),
// signalling the freshness predicate to fall back to the configured
// interval.
func (f *FleetIndex) EnvoyCycle(id string) time.Duration {
	f.mu.RLock()
	ts := f.envoys[id]
	f.mu.RUnlock()
	if ts == nil {
		return 0
	}
	lk := f.lockFor(id)
	lk.Lock()
	cycle := ts.ObservedCycle()
	lk.Unlock()
	return cycle
}

// ObservedCycle returns the smoothed observed inter-CheckIn cadence
// for this envoy state — same shape as FleetIndex.EnvoyCycle but
// callable on a snapshot (e.g. one returned by AllEnvoys) without
// re-locking. Returns 0 when no agent-reported delta has been recorded
// yet (bootstrap).
func (es *EnvoyState) ObservedCycle() time.Duration {
	var maxMs uint32
	for _, ms := range es.recentCheckinDeltasMs {
		if ms > maxMs {
			maxMs = ms
		}
	}
	return time.Duration(maxMs) * time.Millisecond
}

// MarkSeenInMemory updates the LastSeen timestamp in-memory without marking
// the envoy dirty. Used for streaming envoys where the open gRPC stream
// already proves liveness — avoids redundant last_seen DB writes. Also
// clears the offline flag if set. Same per-envoy locking pattern as
// MarkSeen — see there for the rationale.
func (f *FleetIndex) MarkSeenInMemory(id string, t time.Time) {
	f.mu.RLock()
	ts := f.envoys[id]
	f.mu.RUnlock()
	if ts == nil {
		return
	}

	lk := f.lockFor(id)
	lk.Lock()
	ts.LastSeen = t
	wasOffline := ts.Offline
	ts.Offline = false
	lk.Unlock()

	if wasOffline {
		f.convergenceOffline.Add(-1)
	}
}

// UpdateTraits replaces the traits for an envoy. The caller must provide
// already-flattened traits (use FlattenJSON). This also updates the
// byTrait reverse index so trait-based queries stay current.
//
// Takes per-envoy stripe lock for the ts.Traits field write and byTraitMu
// for the byTrait reverse-index mutation — NOT f.mu. The TraitsBatcher
// async flusher is the dominant caller and at fleet scale held f.mu.Lock
// long enough to block every hot-path CheckIn's RLock; separating the
// byTrait domain removes that interaction entirely.
func (f *FleetIndex) UpdateTraits(id string, traits map[string]string) {
	f.mu.RLock()
	ts := f.envoys[id]
	f.mu.RUnlock()
	if ts == nil {
		return
	}

	lk := f.lockFor(id)
	lk.Lock()
	if ts.Revoked {
		lk.Unlock()
		return
	}
	oldTraits := ts.Traits
	ts.Traits = traits
	lk.Unlock()

	f.byTraitMu.Lock()
	f.removeFromTraits(id, oldTraits)
	f.addToTraits(id, traits)
	f.byTraitMu.Unlock()
}

// SetIP updates the envoy's IP address (refreshed on each check-in).
// Returns true iff the new value differs from the previously stored value,
// signalling that the DB row needs to be updated. Callers gate the SQL
// UPDATE on the return so unchanged values don't serialize through the
// WAL writer mutex once per check-in. Also marks the envoy dirty when
// the value changes so the flusher persists the update on its next tick,
// even if the caller's path doesn't itself MarkSeen.
func (f *FleetIndex) SetIP(id, ip string) bool {
	f.mu.RLock()
	ts := f.envoys[id]
	f.mu.RUnlock()
	if ts == nil {
		return false
	}

	lk := f.lockFor(id)
	lk.Lock()
	if ts.IP == ip {
		lk.Unlock()
		return false
	}
	ts.IP = ip
	lk.Unlock()

	f.dirty.Store(id, struct{}{})
	return true
}

// SetAgentVersion records the agent binary version reported during check-in.
// Returns true iff the new value differs from the previously stored value;
// the bool is kept for parity with SetIP but is no longer used by the gRPC
// hot path — the per-checkin DB UPDATE was retired in favor of the flusher's
// value-grouped batched persistence, which collapses N single-row UPDATEs
// during stagger-up into one chunked UPDATE per distinct version. Marks the
// envoy dirty when the value changes so the next flush picks it up.
func (f *FleetIndex) SetAgentVersion(id, version string) bool {
	f.mu.RLock()
	ts := f.envoys[id]
	f.mu.RUnlock()
	if ts == nil {
		return false
	}

	lk := f.lockFor(id)
	lk.Lock()
	if ts.AgentVersion == version {
		lk.Unlock()
		return false
	}
	ts.AgentVersion = version
	lk.Unlock()

	f.dirty.Store(id, struct{}{})
	return true
}

// SetForcePush sets the ForcePush flag.
func (f *FleetIndex) SetForcePush(id string, val bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.ForcePush = val
	f.dirty.Store(id, struct{}{})
}

// MarkRunForced records that the bundle just delivered to this envoy
// was force-pushed, so the run the agent reports back is attributed to
// the operator's force-push rather than a scheduled cycle. Ephemeral —
// ConsumeRunTrigger reads and clears it at run-create time.
func (f *FleetIndex) MarkRunForced(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if ts, ok := f.envoys[id]; ok {
		ts.RunForced = true
	}
}

// ConsumeRunTrigger returns the trigger for the run an envoy is now
// reporting — "force-push" when MarkRunForced armed it (the flag is
// cleared), "scheduled" otherwise. A run is created from exactly one
// report, so the consume is one-shot.
//
// MarkRunForced is rare (only after a force-push delivery) but every
// ReportResult calls ConsumeRunTrigger, so the fast path runs under
// RLock: a missing envoy or an unarmed flag returns "scheduled"
// without touching the write lock. Only an actual hit upgrades to
// Lock + clear, with a re-check inside the critical section in case a
// concurrent Consume cleared the flag between RUnlock and Lock.
func (f *FleetIndex) ConsumeRunTrigger(id string) string {
	f.mu.RLock()
	ts, ok := f.envoys[id]
	if !ok || !ts.RunForced {
		f.mu.RUnlock()
		return "scheduled"
	}
	f.mu.RUnlock()

	f.mu.Lock()
	defer f.mu.Unlock()
	ts, ok = f.envoys[id]
	if !ok || !ts.RunForced {
		return "scheduled"
	}
	ts.RunForced = false
	return "force-push"
}

// SetForceUpdate sets the ForceUpdate flag on a single envoy.
func (f *FleetIndex) SetForceUpdate(id string, val bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.ForceUpdate = val
}

// SetForceUpdateAll sets ForceUpdate on all non-revoked envoys. Returns count.
func (f *FleetIndex) SetForceUpdateAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0
	for _, ts := range f.envoys {
		if ts.Revoked {
			continue
		}
		ts.ForceUpdate = true
		count++
	}
	return count
}

// SetForcePushAll sets ForcePush on all non-revoked envoys. Returns count.
func (f *FleetIndex) SetForcePushAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0
	for id, ts := range f.envoys {
		if ts.Revoked {
			continue
		}
		ts.ForcePush = true
		f.dirty.Store(id, struct{}{})
		count++
	}
	return count
}

// SetStreamRequested sets the StreamRequested flag (ephemeral, not flushed to DB).
func (f *FleetIndex) SetStreamRequested(id string, val bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.StreamRequested = val
	// No dirty mark — this is ephemeral and not persisted to DB.
}

// ClearStreamRequested clears the StreamRequested flag.
func (f *FleetIndex) ClearStreamRequested(id string) {
	f.SetStreamRequested(id, false)
}

// SetTunnelRequested sets the TunnelRequested flag (Scrier).
func (f *FleetIndex) SetTunnelRequested(id string, val bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.TunnelRequested = val
}

// ClearTunnelRequested clears the TunnelRequested flag.
func (f *FleetIndex) ClearTunnelRequested(id string) {
	f.SetTunnelRequested(id, false)
}

// SetFastPoll sets the FastPoll flag so the next check-in response tells
// the agent to come back in 1 second. Any subsystem that needs urgent
// agent contact can call this (Scrier tunnels, task dispatch, secret
// rotation, etc.). The flag is cleared when the check-in response is built.
func (f *FleetIndex) SetFastPoll(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.FastPoll = true
}

// ClearFastPoll clears the FastPoll flag.
func (f *FleetIndex) ClearFastPoll(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.FastPoll = false
}

// RegisterWake creates a wake channel for an envoy. The Wake RPC handler
// blocks on this channel. Returns the channel to select on. If a channel
// already exists (stale connection), it is closed first.
func (f *FleetIndex) RegisterWake(id string) chan struct{} {
	f.wakeMu.Lock()
	defer f.wakeMu.Unlock()
	if old, ok := f.wakeChans[id]; ok {
		select {
		case <-old:
		default:
			close(old)
		}
	}
	ch := make(chan struct{})
	f.wakeChans[id] = ch
	return ch
}

// UnregisterWake removes the wake channel for an envoy.
func (f *FleetIndex) UnregisterWake(id string) {
	f.wakeMu.Lock()
	defer f.wakeMu.Unlock()
	delete(f.wakeChans, id)
}

// SignalWake wakes an agent blocked on a Wake RPC by closing its channel.
// No-op if the agent has no active Wake call.
func (f *FleetIndex) SignalWake(id string) {
	f.wakeMu.Lock()
	defer f.wakeMu.Unlock()
	if ch, ok := f.wakeChans[id]; ok {
		select {
		case <-ch:
			// Already closed.
		default:
			close(ch)
		}
	}
}

// ClearPendingRotatedSecrets clears the pending rotated secrets list.
func (f *FleetIndex) ClearPendingRotatedSecrets(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.PendingRotatedSecrets = nil
	f.dirty.Store(id, struct{}{})
}

// AppendPendingRotatedSecret adds a secret path to a envoy's pending rotation
// list and sets force_push. Used by the secrets API to trigger rotation delivery.
func (f *FleetIndex) AppendPendingRotatedSecret(id, secretPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	// Deduplicate.
	for _, p := range ts.PendingRotatedSecrets {
		if p == secretPath {
			return
		}
	}
	ts.PendingRotatedSecrets = append(ts.PendingRotatedSecrets, secretPath)
	ts.ForcePush = true
	f.dirty.Store(id, struct{}{})
}

// SetTags replaces the tags for a envoy and updates the inverted tag index.
//
// Hot path: buildCheckInResponse calls SetTags on every check-in to sync
// hostcrate tags into the FleetIndex (the hostcrate is the SoT). Tags
// almost never change in steady state, so a fast-path no-op gate under
// f.mu.RLock skips the byTag-mutating slow path entirely; the slow path
// only acquires f.mu.Lock when tags actually changed.
func (f *FleetIndex) SetTags(id string, tags []string) {
	f.mu.RLock()
	ts := f.envoys[id]
	if ts == nil {
		f.mu.RUnlock()
		return
	}
	unchanged := tagsEqual(ts.Tags, tags)
	f.mu.RUnlock()
	if unchanged {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	f.removeFromTags(id, ts.Tags)
	ts.Tags = tags
	if !ts.Revoked {
		f.addToTags(id, tags)
	}
	f.dirty.Store(id, struct{}{})
}

// tagsEqual reports whether two tag slices contain the same set of tags.
// Order is significant — that matches what hostcrates produce (tags come
// from the match block as written, never reordered downstream).
func tagsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}

// SetMaintenanceUntil sets the maintenance window for a envoy.
func (f *FleetIndex) SetMaintenanceUntil(id string, until time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.MaintenanceUntil = until
	f.dirty.Store(id, struct{}{})
}

// ClearMaintenance clears the maintenance window for a envoy.
func (f *FleetIndex) ClearMaintenance(id string) {
	f.SetMaintenanceUntil(id, time.Time{})
}

// UpdateRunStatus updates the last run status fields and recomputes
// convergence using the trailing run history stored on the envoy.
//
// A new [convergence.RunInfo] is prepended to [EnvoyState.RecentRuns]
// (bounded by [MaxRecentRuns]) and the status is recomputed via
// [convergence.ComputeConvergence] — the same algorithm used by the
// historical convergence query, so row status and fleet counts stay
// in lock-step.
//
// Takes f.mu.RLock briefly for envoy lookup, the per-envoy stripe lock
// for the field writes + RecentRuns slice rebuild, byStatusMu only when
// the status actually changes (rare), and atomic ops for the failure-
// and drift-axis counters. No f.mu.Lock at all — at 8K+ combined req/s
// holding the global write lock per ReportResult queued every concurrent
// CheckIn's f.mu.RLock and was the dominant remaining mutex contention
// after the 0.68.6 byTraitMu fix.
func (f *FleetIndex) UpdateRunStatus(id, runID, runStatus string, changedCount, failedCount, moduleCount int64, runAt time.Time) {
	f.mu.RLock()
	ts := f.envoys[id]
	f.mu.RUnlock()
	if ts == nil {
		return
	}

	// Per-envoy state writes under the stripe lock. Capture the
	// status/drift transitions we need to reflect in the fleet-wide
	// counters before releasing the stripe — once it's released, the
	// transition is committed and the counter side can race with
	// another concurrent UpdateRunStatus for a different envoy
	// without consistency issues (each call's counter delta is
	// idempotent and ordered with the per-envoy transition).
	lk := f.lockFor(id)
	lk.Lock()
	ts.LastRunID = runID
	ts.LastRunStatus = runStatus
	ts.LastRunChangedCount = changedCount
	ts.LastRunFailedCount = failedCount
	ts.LastRunModuleCount = moduleCount
	ts.LastRunAt = runAt
	newRuns := make([]convergence.RunInfo, 0, MaxRecentRuns)
	newRuns = append(newRuns, convergence.RunInfo{
		Status:       runStatus,
		ChangedCount: changedCount,
		FailedCount:  failedCount,
		ModuleCount:  moduleCount,
	})
	for i := 0; i < len(ts.RecentRuns) && len(newRuns) < MaxRecentRuns; i++ {
		newRuns = append(newRuns, ts.RecentRuns[i])
	}
	ts.RecentRuns = newRuns
	revoked := ts.Revoked
	var oldStatus, newStatus string
	var oldDrift, newDrift convergence.DriftLevel
	statusChanged := false
	driftChanged := false
	if !revoked {
		oldStatus = ts.ConvergenceStatus
		newStatus = convergence.ComputeConvergence(ts.RecentRuns)
		if newStatus != oldStatus {
			ts.ConvergenceStatus = newStatus
			statusChanged = true
		}
		oldDrift = ts.Drift
		newDrift = convergence.ComputeDrift(ts.RecentRuns)
		if newDrift != oldDrift {
			ts.Drift = newDrift
			driftChanged = true
		}
	}
	lk.Unlock()

	if statusChanged {
		f.byStatusMu.Lock()
		f.removeFromStatus(id, oldStatus)
		f.addToStatus(id, newStatus)
		f.byStatusMu.Unlock()
		f.decrementCounter(oldStatus)
		f.incrementCounter(newStatus)
	}
	if driftChanged {
		f.bumpDriftCounters(oldDrift, newDrift)
	}
}

// AppendRecentRun adds a run summary to the ring buffer. Takes the
// dedicated recentRunsMu — the ring buffer is self-contained, no need to
// hold f.mu and stall every concurrent CheckIn's RLock.
func (f *FleetIndex) AppendRecentRun(run RunSummary) {
	f.recentRunsMu.Lock()
	f.recentRuns.Push(run)
	f.recentRunsMu.Unlock()
}

// MarkDirty marks a envoy for async flush. Lock-free — the sync.Map serves
// as the dirty-set so the hot-path setters that mark dirty don't contend on
// the fleet write lock.
func (f *FleetIndex) MarkDirty(id string) {
	f.dirty.Store(id, struct{}{})
}

// DrainDirty atomically removes every entry from the dirty set and returns a
// snapshot of each entry's EnvoyState. Hot-path setters can continue marking
// dirty in parallel during the drain — anything they Store after the Range
// snapshot is captured by the next flusher tick. Snapshots are taken under
// each envoy's stripe lock so the copy is consistent vs. concurrent field
// writes.
func (f *FleetIndex) DrainDirty() map[string]*EnvoyState {
	var ids []string
	f.dirty.Range(func(key, _ any) bool {
		ids = append(ids, key.(string))
		f.dirty.Delete(key)
		return true
	})
	if len(ids) == 0 {
		return nil
	}

	result := make(map[string]*EnvoyState, len(ids))
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, id := range ids {
		ts, ok := f.envoys[id]
		if !ok {
			continue
		}
		lk := f.lockFor(id)
		lk.Lock()
		cp := *ts
		lk.Unlock()
		result[id] = &cp
	}
	return result
}

// --- Internal helpers (caller must hold the appropriate lock — f.mu.Lock
// for byStatus/byTag/byHostname/counter helpers; byTraitMu.Lock for
// addToTraits/removeFromTraits per the byTraitMu doc comment) ---

func (f *FleetIndex) addToStatus(id, status string) {
	if status == "" {
		status = "no data"
	}
	if f.byStatus[status] == nil {
		f.byStatus[status] = make(map[string]bool)
	}
	f.byStatus[status][id] = true
}

func (f *FleetIndex) removeFromStatus(id, status string) {
	if status == "" {
		status = "no data"
	}
	delete(f.byStatus[status], id)
}

func (f *FleetIndex) addToTraits(id string, traits map[string]string) {
	for key, val := range traits {
		if f.byTrait[key] == nil {
			f.byTrait[key] = make(map[string]map[string]bool)
		}
		if f.byTrait[key][val] == nil {
			f.byTrait[key][val] = make(map[string]bool)
		}
		f.byTrait[key][val][id] = true
	}
}

func (f *FleetIndex) removeFromTraits(id string, traits map[string]string) {
	for key, val := range traits {
		if f.byTrait[key] != nil && f.byTrait[key][val] != nil {
			delete(f.byTrait[key][val], id)
			if len(f.byTrait[key][val]) == 0 {
				delete(f.byTrait[key], val)
			}
			if len(f.byTrait[key]) == 0 {
				delete(f.byTrait, key)
			}
		}
	}
}

func (f *FleetIndex) addToTags(id string, tags []string) {
	for _, tag := range tags {
		if f.byTag[tag] == nil {
			f.byTag[tag] = make(map[string]bool)
		}
		f.byTag[tag][id] = true
	}
}

func (f *FleetIndex) removeFromTags(id string, tags []string) {
	for _, tag := range tags {
		if f.byTag[tag] != nil {
			delete(f.byTag[tag], id)
			if len(f.byTag[tag]) == 0 {
				delete(f.byTag, tag)
			}
		}
	}
}

// incrementCounter bumps the failure-axis bucket matching the given
// status string. Unknown vocabularies fall through ClassifyStatus into
// StatusNoData rather than panicking on the hot path. Drift counters
// move via bumpDriftCounters; this function is failure-axis only.
// incrementCounter / decrementCounter atomically bump the failure-axis
// counter for the given status. Caller does not need any lock — the per-counter
// atomic.Int64 fields are independent of f.mu.
func (f *FleetIndex) incrementCounter(status string) {
	switch convergence.ClassifyStatus(status) {
	case convergence.StatusConverged:
		f.convergenceConverged.Add(1)
	case convergence.StatusDegraded:
		f.convergenceDegraded.Add(1)
	case convergence.StatusFailed:
		f.convergenceFailed.Add(1)
	default:
		f.convergenceNoData.Add(1)
	}
}

func (f *FleetIndex) decrementCounter(status string) {
	switch convergence.ClassifyStatus(status) {
	case convergence.StatusConverged:
		f.convergenceConverged.Add(-1)
	case convergence.StatusDegraded:
		f.convergenceDegraded.Add(-1)
	case convergence.StatusFailed:
		f.convergenceFailed.Add(-1)
	default:
		f.convergenceNoData.Add(-1)
	}
}

// bumpDriftCounters moves the drift-axis counters when an envoy
// transitions between drift levels. Diverged is a strict subset of
// Changed, so an envoy in DriftDiverged contributes to both buckets;
// transitions touch the right combination. Atomic — no lock required.
func (f *FleetIndex) bumpDriftCounters(old, new convergence.DriftLevel) {
	if old == convergence.DriftChanged || old == convergence.DriftDiverged {
		f.convergenceChanged.Add(-1)
	}
	if old == convergence.DriftDiverged {
		f.convergenceDiverged.Add(-1)
	}
	if new == convergence.DriftChanged || new == convergence.DriftDiverged {
		f.convergenceChanged.Add(1)
	}
	if new == convergence.DriftDiverged {
		f.convergenceDiverged.Add(1)
	}
}

// FlattenJSON recursively flattens a nested JSON map into dot-separated keys.
// Arrays are expanded with indexed keys (e.g., cves.0.id, cves.1.severity).
func FlattenJSON(prefix string, data map[string]any, out map[string]string) {
	for key, val := range data {
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "." + key
		}
		switch v := val.(type) {
		case map[string]any:
			FlattenJSON(fullKey, v, out)
		case []any:
			for i, elem := range v {
				elemKey := fullKey + "." + strconv.Itoa(i)
				switch e := elem.(type) {
				case map[string]any:
					FlattenJSON(elemKey, e, out)
				case string:
					// Store string array elements verbatim. Without
					// this branch they fall into the default case
					// below and get re-marshaled as JSON ("dan" →
					// `"dan"` with literal double-quote characters),
					// which breaks every downstream consumer that
					// compares flattened trait values against plain
					// strings — most visibly gitback's
					// envoyHasHumanUser filter.
					out[elemKey] = e
				default:
					if b, err := json.Marshal(e); err == nil {
						out[elemKey] = string(b)
					} else {
						out[elemKey] = fmt.Sprintf("%v", e)
					}
				}
			}
		case string:
			out[fullKey] = v
		default:
			if b, err := json.Marshal(v); err == nil {
				out[fullKey] = string(b)
			} else {
				out[fullKey] = fmt.Sprintf("%v", v)
			}
		}
	}
}

// --- Ring buffer ---

type ringBuffer struct {
	buf  []RunSummary
	cap  int
	head int // next write position
	len  int
}

func newRingBuffer(cap int) *ringBuffer {
	return &ringBuffer{
		buf: make([]RunSummary, cap),
		cap: cap,
	}
}

func (rb *ringBuffer) Push(r RunSummary) {
	rb.buf[rb.head] = r
	rb.head = (rb.head + 1) % rb.cap
	if rb.len < rb.cap {
		rb.len++
	}
}

// Last returns the last n entries, most recent first.
func (rb *ringBuffer) Last(n int) []RunSummary {
	if n > rb.len {
		n = rb.len
	}
	result := make([]RunSummary, n)
	for i := 0; i < n; i++ {
		idx := (rb.head - 1 - i + rb.cap) % rb.cap
		result[i] = rb.buf[idx]
	}
	return result
}

// --- Convergence ring buffer ---

type convergenceRing struct {
	buf  []ConvergenceSnapshot
	cap  int
	head int
	len  int
}

func newConvergenceRing(cap int) *convergenceRing {
	return &convergenceRing{
		buf: make([]ConvergenceSnapshot, cap),
		cap: cap,
	}
}

func (cr *convergenceRing) Push(s ConvergenceSnapshot) {
	cr.buf[cr.head] = s
	cr.head = (cr.head + 1) % cr.cap
	if cr.len < cr.cap {
		cr.len++
	}
}

func (cr *convergenceRing) All() []ConvergenceSnapshot {
	if cr.len == 0 {
		return nil
	}
	result := make([]ConvergenceSnapshot, cr.len)
	start := (cr.head - cr.len + cr.cap) % cr.cap
	for i := 0; i < cr.len; i++ {
		result[i] = cr.buf[(start+i)%cr.cap]
	}
	return result
}
