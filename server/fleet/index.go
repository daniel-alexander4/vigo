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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EnvoyState holds the in-memory state of a single envoy. All fields are
// owned by the FleetIndex and protected by its mutex.
type EnvoyState struct {
	ID                    string
	Hostname              string
	IP                    string
	PubkeyPEM             string
	EnrolledAt            time.Time
	LastSeen              time.Time
	Revoked               bool
	ConvergenceStatus      string
	LastRunStatus         string
	LastRunID             string
	LastRunChangedCount   int64
	LastRunAt             time.Time
	Traits                map[string]string // flattened key→value for trait index
	PendingRotatedSecrets []string
	ForcePush             bool
	ForceUpdate           bool   // ephemeral: agent should download latest binary and restart
	AgentVersion          string // agent binary version reported on check-in
	StreamRequested       bool   // ephemeral: server wants agent to open stream (not persisted to DB)
	TunnelRequested       bool   // ephemeral: server wants agent to open TunnelStream (Scrier)
	FastPoll              bool   // ephemeral: next check-in response uses 1s interval (cleared after delivery)
	Gateway               string // hostname of gateway envoy (empty for normal envoys)
	DeviceType            string // network device type (empty for normal envoys)
	Tags                  []string   // user-assigned tags for targeting
	MaintenanceUntil      time.Time  // zero = not in maintenance
	ObserveOnly           bool       // observe-only mode: report drift only
}

// ConvergenceCounters holds pre-computed convergence totals.
type ConvergenceCounters struct {
	Total     int
	Converged int
	RecurringDrift int
	Errors         int
	LostContact    int
	NoData         int
}

// RunSummary is a brief record of a completed run for the recent-runs ring buffer.
type RunSummary struct {
	RunID        string
	EnvoyID     string
	Hostname     string
	Status       string
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
	envoys    map[string]*EnvoyState       // ID → state
	byHostname map[string]string             // hostname → ID
	pubkeys    map[string]ed25519.PublicKey   // ID → parsed key
	byStatus   map[string]map[string]bool    // status → set of IDs
	byTrait    map[string]map[string]map[string]bool // trait_key → trait_value → set of IDs
	byTag      map[string]map[string]bool              // tag → set of IDs
	convergence ConvergenceCounters
	recentRuns        *ringBuffer
	convergenceHistory *convergenceRing // rolling 30-day daily snapshots
	dirty             map[string]bool // IDs with pending state changes

	// Wake channels — separate mutex so blocking Wake RPCs don't hold the main lock.
	wakeMu    sync.Mutex
	wakeChans map[string]chan struct{} // envoy_id → signal channel
}

// NewFleetIndex creates an empty FleetIndex.
func NewFleetIndex() *FleetIndex {
	return &FleetIndex{
		envoys:    make(map[string]*EnvoyState),
		byHostname: make(map[string]string),
		pubkeys:    make(map[string]ed25519.PublicKey),
		byStatus:   make(map[string]map[string]bool),
		byTrait:    make(map[string]map[string]map[string]bool),
		byTag:      make(map[string]map[string]bool),
		recentRuns:        newRingBuffer(5000),
		convergenceHistory: newConvergenceRing(30),
		dirty:             make(map[string]bool),
		wakeChans:         make(map[string]chan struct{}),
	}
}

// --- Read methods (acquire RLock) ---

// GetEnvoy returns a copy of the EnvoyState for the given ID.
// Returns nil if not found.
func (f *FleetIndex) GetEnvoy(id string) *EnvoyState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ts := f.envoys[id]
	if ts == nil {
		return nil
	}
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
	return &cp
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
	cp := *src // shallow copy — shares Traits/PendingRotatedSecrets slices
	return &cp, true, src.Revoked
}

// GetPubKey returns the parsed ED25519 public key for the given envoy ID.
// Returns nil if not found.
func (f *FleetIndex) GetPubKey(id string) ed25519.PublicKey {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.pubkeys[id]
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
func (f *FleetIndex) ListByTrait(key, value string) []TargetInfo {
	f.mu.RLock()
	defer f.mu.RUnlock()
	valMap, ok := f.byTrait[key]
	if !ok {
		return nil
	}
	ids, ok := valMap[value]
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

// TargetInfo is the minimal info returned by targeting queries.
type TargetInfo struct {
	EnvoyID string
	Hostname string
}

// ConvergenceSummary returns the pre-computed convergence counters.
func (f *FleetIndex) ConvergenceSummary() ConvergenceCounters {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.convergence
}

// ConvergenceCounts returns the convergence breakdown as flat values.
// Satisfies the spanner.ConvergenceSource interface.
func (f *FleetIndex) ConvergenceCounts() (total, compliant, recurringDrift, errors, lostContact, noData int) {
	c := f.ConvergenceSummary()
	return c.Total, c.Converged, c.RecurringDrift, c.Errors, c.LostContact, c.NoData
}

// SnapshotConvergence records a convergence snapshot for the sparkline.
func (f *FleetIndex) SnapshotConvergence() {
	f.mu.RLock()
	snap := ConvergenceSnapshot{
		Timestamp: time.Now(),
		Counters:  f.convergence,
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
	for _, ts := range f.envoys {
		result = append(result, *ts)
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
	for _, ts := range f.envoys {
		if ts.Revoked {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(ts.Hostname), searchLower) {
			continue
		}
		if statusFilter != "" && ts.ConvergenceStatus != statusFilter {
			continue
		}
		if afterHostname != "" && ts.Hostname <= afterHostname {
			continue
		}
		candidates = append(candidates, *ts)
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

// RecentRuns returns the last n run summaries (most recent first).
func (f *FleetIndex) RecentRuns(n int) []RunSummary {
	f.mu.RLock()
	defer f.mu.RUnlock()
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
	defer f.mu.RUnlock()
	if f.convergence.Total == 0 {
		return 100.0
	}
	return float64(f.convergence.Converged) / float64(f.convergence.Total) * 100.0
}

// --- Write methods (acquire Lock) ---

// AddEnvoy inserts a new envoy into the index. If a envoy with the same ID
// already exists, it is overwritten.
func (f *FleetIndex) AddEnvoy(ts EnvoyState, pubKey ed25519.PublicKey) {
	f.mu.Lock()
	defer f.mu.Unlock()

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
		}
	}

	state := ts // copy
	f.envoys[ts.ID] = &state
	f.byHostname[ts.Hostname] = ts.ID
	if pubKey != nil {
		f.pubkeys[ts.ID] = pubKey
	}

	if !ts.Revoked {
		f.addToStatus(ts.ID, ts.ConvergenceStatus)
		f.addToTraits(ts.ID, ts.Traits)
		f.addToTags(ts.ID, ts.Tags)
		f.convergence.Total++
		f.incrementCounter(ts.ConvergenceStatus)
	}
}

// RemoveEnvoy removes a envoy from the index entirely.
func (f *FleetIndex) RemoveEnvoy(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}

	delete(f.byHostname, ts.Hostname)
	delete(f.pubkeys, id)
	f.removeFromStatus(id, ts.ConvergenceStatus)
	f.removeFromTraits(id, ts.Traits)
	f.removeFromTags(id, ts.Tags)
	if !ts.Revoked {
		f.convergence.Total--
		f.decrementCounter(ts.ConvergenceStatus)
	}
	delete(f.envoys, id)
	delete(f.dirty, id)
}

// SetRevoked marks a envoy as revoked.
func (f *FleetIndex) SetRevoked(id string, revoked bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

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
	} else if wasRevoked && !revoked {
		// Becoming active again: add to counters.
		f.addToStatus(id, ts.ConvergenceStatus)
		f.convergence.Total++
		f.incrementCounter(ts.ConvergenceStatus)
	}
}

// MarkSeen updates the LastSeen timestamp for a envoy and marks it dirty
// for async flush to SQLite.
func (f *FleetIndex) MarkSeen(id string, t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.LastSeen = t
	f.dirty[id] = true
}

// MarkSeenInMemory updates the LastSeen timestamp in-memory without marking
// the envoy dirty. Used for streaming envoys where the open gRPC stream
// already proves liveness — avoids redundant last_seen DB writes.
func (f *FleetIndex) MarkSeenInMemory(id string, t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.LastSeen = t
}

// UpdateTraits replaces the traits for an envoy. The caller must provide
// already-flattened traits (use FlattenJSON). This also updates the
// byTrait reverse index so trait-based queries stay current.
func (f *FleetIndex) UpdateTraits(id string, traits map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok || ts.Revoked {
		return
	}
	f.removeFromTraits(id, ts.Traits)
	ts.Traits = traits
	f.addToTraits(id, traits)
}

// SetAgentVersion records the agent binary version reported during check-in.
func (f *FleetIndex) SetAgentVersion(id, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}
	ts.AgentVersion = version
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
	f.dirty[id] = true
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
		f.dirty[id] = true
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
	f.dirty[id] = true
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
	f.dirty[id] = true
}

// SetTags replaces the tags for a envoy and updates the inverted tag index.
func (f *FleetIndex) SetTags(id string, tags []string) {
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
	f.dirty[id] = true
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
	f.dirty[id] = true
}

// ClearMaintenance clears the maintenance window for a envoy.
func (f *FleetIndex) ClearMaintenance(id string) {
	f.SetMaintenanceUntil(id, time.Time{})
}

// UpdateRunStatus updates the last run status fields and recomputes convergence.
func (f *FleetIndex) UpdateRunStatus(id, runID, runStatus string, changedCount int64, runAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.envoys[id]
	if !ok {
		return
	}

	ts.LastRunID = runID
	ts.LastRunStatus = runStatus
	ts.LastRunChangedCount = changedCount
	ts.LastRunAt = runAt

	if !ts.Revoked {
		oldStatus := ts.ConvergenceStatus
		newStatus := computeInlineStatus(runStatus, changedCount)
		if newStatus != oldStatus {
			f.removeFromStatus(id, oldStatus)
			f.decrementCounter(oldStatus)
			ts.ConvergenceStatus = newStatus
			f.addToStatus(id, newStatus)
			f.incrementCounter(newStatus)
		}
	}
}

// AppendRecentRun adds a run summary to the ring buffer.
func (f *FleetIndex) AppendRecentRun(run RunSummary) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recentRuns.Push(run)
}

// MarkDirty marks a envoy for async flush.
func (f *FleetIndex) MarkDirty(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirty[id] = true
}

// DrainDirty returns and clears the set of dirty envoy IDs.
func (f *FleetIndex) DrainDirty() map[string]*EnvoyState {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.dirty) == 0 {
		return nil
	}

	result := make(map[string]*EnvoyState, len(f.dirty))
	for id := range f.dirty {
		if ts, ok := f.envoys[id]; ok {
			cp := *ts
			result[id] = &cp
		}
	}
	f.dirty = make(map[string]bool)
	return result
}

// --- Internal helpers (caller must hold lock) ---

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

func (f *FleetIndex) incrementCounter(status string) {
	switch status {
	case "converged":
		f.convergence.Converged++
	case "recurring drift":
		f.convergence.RecurringDrift++
	case "errors":
		f.convergence.Errors++
	case "lost contact":
		f.convergence.LostContact++
	default:
		f.convergence.NoData++
	}
}

func (f *FleetIndex) decrementCounter(status string) {
	switch status {
	case "converged":
		f.convergence.Converged--
	case "recurring drift":
		f.convergence.RecurringDrift--
	case "errors":
		f.convergence.Errors--
	case "lost contact":
		f.convergence.LostContact--
	default:
		f.convergence.NoData--
	}
}

// computeInlineStatus mirrors convergence.ComputeConvergence logic but only for
// run-based status (stale detection is handled separately by the stale checker).
func computeInlineStatus(runStatus string, changedCount int64) string {
	switch runStatus {
	case "success":
		if changedCount == 0 {
			return "converged"
		}
		return "drifted"
	case "failure", "partial":
		return "failing"
	default:
		return "unknown"
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
				default:
					b, _ := json.Marshal(e)
					out[elemKey] = string(b)
				}
			}
		case string:
			out[fullKey] = v
		default:
			b, _ := json.Marshal(v)
			out[fullKey] = string(b)
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
