// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package fleet provides an in-memory FleetIndex that holds the current state
// of every thrall. All real-time queries (pubkey lookups, check-in state reads,
// targeting, compliance counters) are served from this index. SQLite remains
// the durable store, fed asynchronously via the StateFlusher.
package fleet

import (
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ThrallState holds the in-memory state of a single thrall. All fields are
// owned by the FleetIndex and protected by its mutex.
type ThrallState struct {
	ID                    string
	Hostname              string
	IP                    string
	PubkeyPEM             string
	EnrolledAt            time.Time
	LastSeen              time.Time
	Revoked               bool
	ComplianceStatus      string
	LastRunStatus         string
	LastRunChangedCount   int64
	LastRunAt             time.Time
	Traits                map[string]string // flattened key→value for trait index
	PendingRotatedSecrets []string
	ForcePush             bool
	ForceUpdate           bool   // ephemeral: agent should download latest binary and restart
	AgentVersion          string // agent binary version reported on check-in
	StreamRequested       bool   // ephemeral: server wants agent to open stream (not persisted to DB)
	Gateway               string // hostname of gateway thrall (empty for normal thralls)
	DeviceType            string // network device type (empty for normal thralls)
	Tags                  []string   // user-assigned tags for targeting
	MaintenanceUntil      time.Time  // zero = not in maintenance
}

// ComplianceCounters holds pre-computed compliance totals.
type ComplianceCounters struct {
	Total     int
	Compliant int
	RecurringDrift int
	Errors         int
	LostContact    int
	NoData         int
}

// RunSummary is a brief record of a completed run for the recent-runs ring buffer.
type RunSummary struct {
	RunID        string
	ThrallID     string
	Hostname     string
	Status       string
	ChangedCount int64
	FailedCount  int64
	FinishedAt   time.Time
}

// ComplianceSnapshot is a daily compliance snapshot for the sparkline.
type ComplianceSnapshot struct {
	Timestamp time.Time
	Counters  ComplianceCounters
}

// FleetIndex is a concurrency-safe in-memory index of all thralls. It provides
// O(1) lookups by ID, hostname, and pubkey, O(k) queries by status and trait,
// and pre-computed compliance counters. Write methods mark thralls dirty for
// async flush to SQLite.
type FleetIndex struct {
	mu         sync.RWMutex
	thralls    map[string]*ThrallState       // ID → state
	byHostname map[string]string             // hostname → ID
	pubkeys    map[string]ed25519.PublicKey   // ID → parsed key
	byStatus   map[string]map[string]bool    // status → set of IDs
	byTrait    map[string]map[string]map[string]bool // trait_key → trait_value → set of IDs
	byTag      map[string]map[string]bool              // tag → set of IDs
	compliance ComplianceCounters
	recentRuns        *ringBuffer
	complianceHistory *complianceRing // rolling 30-day daily snapshots
	dirty             map[string]bool // IDs with pending state changes
}

// NewFleetIndex creates an empty FleetIndex.
func NewFleetIndex() *FleetIndex {
	return &FleetIndex{
		thralls:    make(map[string]*ThrallState),
		byHostname: make(map[string]string),
		pubkeys:    make(map[string]ed25519.PublicKey),
		byStatus:   make(map[string]map[string]bool),
		byTrait:    make(map[string]map[string]map[string]bool),
		byTag:      make(map[string]map[string]bool),
		recentRuns:        newRingBuffer(5000),
		complianceHistory: newComplianceRing(30),
		dirty:             make(map[string]bool),
	}
}

// --- Read methods (acquire RLock) ---

// GetThrall returns a copy of the ThrallState for the given ID.
// Returns nil if not found.
func (f *FleetIndex) GetThrall(id string) *ThrallState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ts := f.thralls[id]
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

// GetThrallLite returns a shallow copy of the ThrallState without deep-copying
// Traits or PendingRotatedSecrets. It also returns existence and revoked status,
// collapsing IsRevoked + GetThrall into one lock acquisition. The caller must
// not mutate the Traits or PendingRotatedSecrets slices on the returned value.
func (f *FleetIndex) GetThrallLite(id string) (ts *ThrallState, exists, revoked bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	src := f.thralls[id]
	if src == nil {
		return nil, false, false
	}
	cp := *src // shallow copy — shares Traits/PendingRotatedSecrets slices
	return &cp, true, src.Revoked
}

// GetPubKey returns the parsed ED25519 public key for the given thrall ID.
// Returns nil if not found.
func (f *FleetIndex) GetPubKey(id string) ed25519.PublicKey {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.pubkeys[id]
}

// GetByHostname returns the thrall ID for the given hostname.
// Returns "" if not found.
func (f *FleetIndex) GetByHostname(hostname string) string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.byHostname[hostname]
}

// ListByGlob returns all non-revoked thralls whose hostname matches the glob pattern.
func (f *FleetIndex) ListByGlob(pattern string) []TargetInfo {
	f.mu.RLock()
	defer f.mu.RUnlock()
	// Pre-allocate for ~10% match rate to avoid repeated slice growth.
	estimate := len(f.thralls) / 10
	if estimate < 8 {
		estimate = 8
	}
	results := make([]TargetInfo, 0, estimate)
	for _, ts := range f.thralls {
		if ts.Revoked {
			continue
		}
		if matched, _ := filepath.Match(pattern, ts.Hostname); matched {
			results = append(results, TargetInfo{ThrallID: ts.ID, Hostname: ts.Hostname})
		}
	}
	return results
}

// ListByTrait returns all non-revoked thralls whose flattened traits contain key=value.
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
		ts := f.thralls[id]
		if ts != nil && !ts.Revoked {
			results = append(results, TargetInfo{ThrallID: ts.ID, Hostname: ts.Hostname})
		}
	}
	return results
}

// TargetInfo is the minimal info returned by targeting queries.
type TargetInfo struct {
	ThrallID string
	Hostname string
}

// ComplianceSummary returns the pre-computed compliance counters.
func (f *FleetIndex) ComplianceSummary() ComplianceCounters {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.compliance
}

// ComplianceCounts returns the compliance breakdown as flat values.
// Satisfies the spanner.ComplianceSource interface.
func (f *FleetIndex) ComplianceCounts() (total, compliant, recurringDrift, errors, lostContact, noData int) {
	c := f.ComplianceSummary()
	return c.Total, c.Compliant, c.RecurringDrift, c.Errors, c.LostContact, c.NoData
}

// SnapshotCompliance records a compliance snapshot for the sparkline.
func (f *FleetIndex) SnapshotCompliance() {
	f.mu.RLock()
	snap := ComplianceSnapshot{
		Timestamp: time.Now(),
		Counters:  f.compliance,
	}
	f.mu.RUnlock()

	f.mu.Lock()
	f.complianceHistory.Push(snap)
	f.mu.Unlock()
}

// ComplianceSparkline returns the rolling compliance history snapshots.
func (f *FleetIndex) ComplianceSparkline() []ComplianceSnapshot {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.complianceHistory.All()
}

// AllThralls returns a copy of all ThrallState values.
func (f *FleetIndex) AllThralls() []ThrallState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	result := make([]ThrallState, 0, len(f.thralls))
	for _, ts := range f.thralls {
		result = append(result, *ts)
	}
	return result
}

// PaginatedThralls returns up to `limit` non-revoked thralls, sorted by hostname,
// starting after `afterHostname` (exclusive). Pass "" to start from the beginning.
// Returns the thralls and whether there are more results.
func (f *FleetIndex) PaginatedThralls(afterHostname string, limit int, statusFilter string) ([]ThrallState, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// Collect matching thralls.
	var candidates []ThrallState
	for _, ts := range f.thralls {
		if ts.Revoked {
			continue
		}
		if statusFilter != "" && ts.ComplianceStatus != statusFilter {
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

// ListByTag returns all non-revoked thralls with the given tag.
func (f *FleetIndex) ListByTag(tag string) []TargetInfo {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ids, ok := f.byTag[tag]
	if !ok {
		return nil
	}
	var results []TargetInfo
	for id := range ids {
		ts := f.thralls[id]
		if ts != nil && !ts.Revoked {
			results = append(results, TargetInfo{ThrallID: ts.ID, Hostname: ts.Hostname})
		}
	}
	return results
}

// IsInMaintenance returns true if the thrall is currently in a maintenance window.
func (f *FleetIndex) IsInMaintenance(id string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ts := f.thralls[id]
	if ts == nil {
		return false
	}
	return !ts.MaintenanceUntil.IsZero() && time.Now().Before(ts.MaintenanceUntil)
}

// CompliancePercent returns the percentage of non-revoked thralls that are compliant.
func (f *FleetIndex) CompliancePercent() float64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.compliance.Total == 0 {
		return 100.0
	}
	return float64(f.compliance.Compliant) / float64(f.compliance.Total) * 100.0
}

// --- Write methods (acquire Lock) ---

// AddThrall inserts a new thrall into the index. If a thrall with the same ID
// already exists, it is overwritten.
func (f *FleetIndex) AddThrall(ts ThrallState, pubKey ed25519.PublicKey) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Remove old hostname mapping if it changed.
	if old, ok := f.thralls[ts.ID]; ok {
		if old.Hostname != ts.Hostname {
			delete(f.byHostname, old.Hostname)
		}
		f.removeFromStatus(ts.ID, old.ComplianceStatus)
		f.removeFromTraits(ts.ID, old.Traits)
		f.removeFromTags(ts.ID, old.Tags)
		if !old.Revoked {
			f.compliance.Total--
			f.decrementCounter(old.ComplianceStatus)
		}
	}

	state := ts // copy
	f.thralls[ts.ID] = &state
	f.byHostname[ts.Hostname] = ts.ID
	if pubKey != nil {
		f.pubkeys[ts.ID] = pubKey
	}

	if !ts.Revoked {
		f.addToStatus(ts.ID, ts.ComplianceStatus)
		f.addToTraits(ts.ID, ts.Traits)
		f.addToTags(ts.ID, ts.Tags)
		f.compliance.Total++
		f.incrementCounter(ts.ComplianceStatus)
	}
}

// RemoveThrall removes a thrall from the index entirely.
func (f *FleetIndex) RemoveThrall(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
	if !ok {
		return
	}

	delete(f.byHostname, ts.Hostname)
	delete(f.pubkeys, id)
	f.removeFromStatus(id, ts.ComplianceStatus)
	f.removeFromTraits(id, ts.Traits)
	f.removeFromTags(id, ts.Tags)
	if !ts.Revoked {
		f.compliance.Total--
		f.decrementCounter(ts.ComplianceStatus)
	}
	delete(f.thralls, id)
	delete(f.dirty, id)
}

// SetRevoked marks a thrall as revoked.
func (f *FleetIndex) SetRevoked(id string, revoked bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
	if !ok {
		return
	}

	wasRevoked := ts.Revoked
	ts.Revoked = revoked

	if !wasRevoked && revoked {
		// Becoming revoked: remove from active counters.
		f.removeFromStatus(id, ts.ComplianceStatus)
		f.compliance.Total--
		f.decrementCounter(ts.ComplianceStatus)
	} else if wasRevoked && !revoked {
		// Becoming active again: add to counters.
		f.addToStatus(id, ts.ComplianceStatus)
		f.compliance.Total++
		f.incrementCounter(ts.ComplianceStatus)
	}
}

// MarkSeen updates the LastSeen timestamp for a thrall and marks it dirty
// for async flush to SQLite.
func (f *FleetIndex) MarkSeen(id string, t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
	if !ok {
		return
	}
	ts.LastSeen = t
	f.dirty[id] = true
}

// MarkSeenInMemory updates the LastSeen timestamp in-memory without marking
// the thrall dirty. Used for streaming thralls where the open gRPC stream
// already proves liveness — avoids redundant last_seen DB writes.
func (f *FleetIndex) MarkSeenInMemory(id string, t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
	if !ok {
		return
	}
	ts.LastSeen = t
}

// SetAgentVersion records the agent binary version reported during check-in.
func (f *FleetIndex) SetAgentVersion(id, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
	if !ok {
		return
	}
	ts.AgentVersion = version
}

// SetForcePush sets the ForcePush flag.
func (f *FleetIndex) SetForcePush(id string, val bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
	if !ok {
		return
	}
	ts.ForcePush = val
	f.dirty[id] = true
}

// SetForceUpdate sets the ForceUpdate flag on a single thrall.
func (f *FleetIndex) SetForceUpdate(id string, val bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
	if !ok {
		return
	}
	ts.ForceUpdate = val
}

// SetForceUpdateAll sets ForceUpdate on all non-revoked thralls. Returns count.
func (f *FleetIndex) SetForceUpdateAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0
	for _, ts := range f.thralls {
		if ts.Revoked {
			continue
		}
		ts.ForceUpdate = true
		count++
	}
	return count
}

// SetForcePushAll sets ForcePush on all non-revoked thralls. Returns count.
func (f *FleetIndex) SetForcePushAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0
	for id, ts := range f.thralls {
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

	ts, ok := f.thralls[id]
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

// ClearPendingRotatedSecrets clears the pending rotated secrets list.
func (f *FleetIndex) ClearPendingRotatedSecrets(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
	if !ok {
		return
	}
	ts.PendingRotatedSecrets = nil
	f.dirty[id] = true
}

// AppendPendingRotatedSecret adds a secret path to a thrall's pending rotation
// list and sets force_push. Used by the secrets API to trigger rotation delivery.
func (f *FleetIndex) AppendPendingRotatedSecret(id, secretPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
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

// SetTags replaces the tags for a thrall and updates the inverted tag index.
func (f *FleetIndex) SetTags(id string, tags []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
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

// SetMaintenanceUntil sets the maintenance window for a thrall.
func (f *FleetIndex) SetMaintenanceUntil(id string, until time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
	if !ok {
		return
	}
	ts.MaintenanceUntil = until
	f.dirty[id] = true
}

// ClearMaintenance clears the maintenance window for a thrall.
func (f *FleetIndex) ClearMaintenance(id string) {
	f.SetMaintenanceUntil(id, time.Time{})
}

// UpdateRunStatus updates the last run status fields and recomputes compliance.
func (f *FleetIndex) UpdateRunStatus(id, runStatus string, changedCount int64, runAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts, ok := f.thralls[id]
	if !ok {
		return
	}

	ts.LastRunStatus = runStatus
	ts.LastRunChangedCount = changedCount
	ts.LastRunAt = runAt

	if !ts.Revoked {
		oldStatus := ts.ComplianceStatus
		newStatus := computeInlineStatus(runStatus, changedCount)
		if newStatus != oldStatus {
			f.removeFromStatus(id, oldStatus)
			f.decrementCounter(oldStatus)
			ts.ComplianceStatus = newStatus
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

// MarkDirty marks a thrall for async flush.
func (f *FleetIndex) MarkDirty(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirty[id] = true
}

// DrainDirty returns and clears the set of dirty thrall IDs.
func (f *FleetIndex) DrainDirty() map[string]*ThrallState {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.dirty) == 0 {
		return nil
	}

	result := make(map[string]*ThrallState, len(f.dirty))
	for id := range f.dirty {
		if ts, ok := f.thralls[id]; ok {
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
	case "compliant":
		f.compliance.Compliant++
	case "recurring drift":
		f.compliance.RecurringDrift++
	case "errors":
		f.compliance.Errors++
	case "lost contact":
		f.compliance.LostContact++
	default:
		f.compliance.NoData++
	}
}

func (f *FleetIndex) decrementCounter(status string) {
	switch status {
	case "compliant":
		f.compliance.Compliant--
	case "recurring drift":
		f.compliance.RecurringDrift--
	case "errors":
		f.compliance.Errors--
	case "lost contact":
		f.compliance.LostContact--
	default:
		f.compliance.NoData--
	}
}

// computeInlineStatus mirrors compliance.ComputeStatus logic but only for
// run-based status (stale detection is handled separately by the stale checker).
func computeInlineStatus(runStatus string, changedCount int64) string {
	switch runStatus {
	case "success":
		if changedCount == 0 {
			return "compliant"
		}
		return "drifted"
	case "failure", "partial":
		return "failing"
	default:
		return "unknown"
	}
}

// flattenJSON recursively flattens a nested JSON map into dot-separated keys.
func flattenJSON(prefix string, data map[string]any, out map[string]string) {
	for key, val := range data {
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "." + key
		}
		switch v := val.(type) {
		case map[string]any:
			flattenJSON(fullKey, v, out)
		case string:
			out[fullKey] = v
		default:
			// Convert numbers, bools, etc. to string.
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

// --- Compliance ring buffer ---

type complianceRing struct {
	buf  []ComplianceSnapshot
	cap  int
	head int
	len  int
}

func newComplianceRing(cap int) *complianceRing {
	return &complianceRing{
		buf: make([]ComplianceSnapshot, cap),
		cap: cap,
	}
}

func (cr *complianceRing) Push(s ComplianceSnapshot) {
	cr.buf[cr.head] = s
	cr.head = (cr.head + 1) % cr.cap
	if cr.len < cr.cap {
		cr.len++
	}
}

func (cr *complianceRing) All() []ComplianceSnapshot {
	if cr.len == 0 {
		return nil
	}
	result := make([]ComplianceSnapshot, cr.len)
	start := (cr.head - cr.len + cr.cap) % cr.cap
	for i := 0; i < cr.len; i++ {
		result[i] = cr.buf[(start+i)%cr.cap]
	}
	return result
}
