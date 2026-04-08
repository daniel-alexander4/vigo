// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"strings"
	"sync"
	"time"
)

// PeerState tracks what an envoy has for a specific blob.
type PeerState struct {
	Address      string            // ip:port (e.g., "10.0.1.5:1531")
	Chunks       []uint32          // chunk indices (nil = all chunks / complete)
	Complete     bool              // true if the envoy has the full blob
	FirstSeen    time.Time         // when this envoy first reported for this blob
	LastSeen     time.Time         // last time this envoy reported
	ChunkSources  map[string]int    // source address → chunk count (set when complete)
	ChunkDetails  []ChunkDetail     // per-chunk source and timing
}

// ChunkDetail records how a single chunk was downloaded.
type ChunkDetail struct {
	Chunk      int    `json:"chunk"`
	Source     string `json:"source"`
	DurationMs int    `json:"ms"`
}

// AssignmentState tracks a pending blob assignment for an envoy.
type AssignmentState struct {
	SHA256        string
	Size          uint64
	Label         string
	Retention     time.Duration // agent auto-evicts after this duration (0 = keep forever)
	VigosyncUser  string        // vigosync: target username (empty = normal swarm)
	VigosyncMtime int64         // vigosync: source mtime (Unix seconds)
}

// EnvoySwarmStatus is the per-envoy status returned by the status API.
type EnvoySwarmStatus struct {
	Hostname     string            `json:"hostname"`
	Status       string            `json:"status"`      // "complete", "downloading", "pending", "skipped"
	ChunksHave   int               `json:"chunks_have"` // number of chunks downloaded
	ChunksNeed   int               `json:"chunks_need"` // total chunks in the blob
	SkipReason   string            `json:"skip_reason,omitempty"`
	StartedAt    time.Time         `json:"started_at,omitempty"`    // when the envoy first reported (start of download)
	CompletedAt  time.Time         `json:"completed_at,omitempty"`  // when the envoy finished downloading (zero if not complete)
	ChunkSources  map[string]int    `json:"chunk_sources,omitempty"`  // source address → chunk count
	ChunkDetails  []ChunkDetail     `json:"chunk_details,omitempty"`  // per-chunk source and timing
	IsSource     bool              `json:"is_source,omitempty"`     // true if this envoy is the original seeder
}

// SeedState tracks an envoy-originated blob that was auto-distributed.
type SeedState struct {
	Label         string
	Size          uint64
	Target        string
	Retention     time.Duration
	SeederID      string
	DistributedAt time.Time
	Retired       bool
	RetiredAt     time.Time
}

// BrokenSeed represents a seed file with an invalid or missing manifest.
type BrokenSeed struct {
	Filename string `json:"filename"`
	Error    string `json:"error"`
}

// PeerTracker maintains an in-memory map of which envoys have which blobs
// (or chunks thereof) and which envoys have pending blob assignments.
type PeerTracker struct {
	mu sync.RWMutex

	// peers maps sha256 → envoyID → PeerState.
	peers map[string]map[string]*PeerState

	// assignments maps envoyID → list of pending blob assignments.
	assignments map[string][]AssignmentState

	// skipped maps sha256 → envoyID → reason.
	skipped map[string]map[string]string

	// seeds maps sha256 → SeedState for envoy-originated blobs.
	seeds map[string]SeedState

	// brokenSeeds maps envoyID → list of broken seed files (refreshed each check-in).
	brokenSeeds map[string][]BrokenSeed

	// staleThreshold is how long before a peer entry expires.
	staleThreshold time.Duration
}

// NewPeerTracker creates a peer tracker. staleThreshold is typically 2× the
// base check-in interval.
func NewPeerTracker(staleThreshold time.Duration) *PeerTracker {
	return &PeerTracker{
		peers:          make(map[string]map[string]*PeerState),
		assignments:    make(map[string][]AssignmentState),
		skipped:        make(map[string]map[string]string),
		seeds:          make(map[string]SeedState),
		brokenSeeds:    make(map[string][]BrokenSeed),
		staleThreshold: staleThreshold,
	}
}

// ReportChunks updates the tracker with an envoy's blob availability.
// Called when the swarm.blobs trait is ingested during check-in.
func (t *PeerTracker) ReportChunks(envoyID, sha256, address string, chunks []uint32, complete bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	blobPeers, ok := t.peers[sha256]
	if !ok {
		blobPeers = make(map[string]*PeerState)
		t.peers[sha256] = blobPeers
	}
	now := time.Now()
	if existing, ok := blobPeers[envoyID]; ok {
		// Update existing — preserve FirstSeen and ChunkSources.
		existing.Address = address
		existing.Chunks = chunks
		// Only update LastSeen if not already complete — once complete,
		// LastSeen freezes so the UI shows a static distribution duration.
		if !existing.Complete {
			existing.LastSeen = now
		}
		existing.Complete = complete
	} else {
		blobPeers[envoyID] = &PeerState{
			Address:   address,
			Chunks:    chunks,
			Complete:  complete,
			FirstSeen: now,
			LastSeen:  now,
		}
	}
}

// ReportComplete marks an envoy as having the full blob.
func (t *PeerTracker) ReportComplete(envoyID, sha256, address string) {
	t.ReportChunks(envoyID, sha256, address, nil, true)
}

// ReportSkipped records that an envoy skipped a blob (e.g., insufficient disk).
func (t *PeerTracker) ReportSkipped(envoyID, sha256, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	blobSkipped, ok := t.skipped[sha256]
	if !ok {
		blobSkipped = make(map[string]string)
		t.skipped[sha256] = blobSkipped
	}
	blobSkipped[envoyID] = reason
}

// GetPeers returns the list of peers that have (chunks of) the given blob,
// excluding stale entries and the requesting envoy itself.
func (t *PeerTracker) GetPeers(sha256, excludeEnvoyID string) []PeerState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	blobPeers := t.peers[sha256]
	if len(blobPeers) == 0 {
		return nil
	}

	now := time.Now()
	var result []PeerState
	for id, ps := range blobPeers {
		if id == excludeEnvoyID {
			continue
		}
		if now.Sub(ps.LastSeen) > t.staleThreshold {
			continue
		}
		result = append(result, *ps)
	}
	return result
}

// Assign adds a blob assignment for an envoy. The assignment will be included
// in the next check-in response.
func (t *PeerTracker) Assign(envoyID string, a AssignmentState) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Deduplicate: don't add the same blob twice.
	for _, existing := range t.assignments[envoyID] {
		if existing.SHA256 == a.SHA256 {
			return
		}
	}
	t.assignments[envoyID] = append(t.assignments[envoyID], a)
}

// PopAssignments returns and removes all pending assignments for an envoy.
// Only returns assignments for blobs the envoy hasn't completed yet.
func (t *PeerTracker) PopAssignments(envoyID string) []AssignmentState {
	t.mu.Lock()
	defer t.mu.Unlock()

	assignments := t.assignments[envoyID]
	if len(assignments) == 0 {
		return nil
	}

	// Filter out assignments for blobs the envoy already has.
	var pending []AssignmentState
	for _, a := range assignments {
		if blobPeers := t.peers[a.SHA256]; blobPeers != nil {
			if ps := blobPeers[envoyID]; ps != nil && ps.Complete {
				continue // already has it
			}
		}
		pending = append(pending, a)
	}

	// Don't delete completed assignments from the list — keep them so
	// they're re-sent if the envoy re-enrolls. Just filter on read.
	return pending
}

// Status returns per-envoy status for a blob. Used by the status API.
func (t *PeerTracker) Status(sha256 string, totalChunks int, seederID string) []EnvoySwarmStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	seen := make(map[string]bool)
	var result []EnvoySwarmStatus

	// Envoys that have reported chunks.
	if blobPeers := t.peers[sha256]; blobPeers != nil {
		for envoyID, ps := range blobPeers {
			seen[envoyID] = true
			st := "downloading"
			have := len(ps.Chunks)
			var completedAt time.Time
			if ps.Complete {
				st = "complete"
				have = totalChunks
				completedAt = ps.LastSeen
			}
			result = append(result, EnvoySwarmStatus{
				Hostname:     envoyID,
				Status:       st,
				ChunksHave:   have,
				ChunksNeed:   totalChunks,
				StartedAt:    ps.FirstSeen,
				CompletedAt:  completedAt,
				ChunkSources: ps.ChunkSources,
				ChunkDetails: ps.ChunkDetails,
				IsSource:     envoyID == seederID,
			})
		}
	}

	// Envoys that skipped.
	if blobSkipped := t.skipped[sha256]; blobSkipped != nil {
		for envoyID, reason := range blobSkipped {
			if seen[envoyID] {
				continue
			}
			seen[envoyID] = true
			result = append(result, EnvoySwarmStatus{
				Hostname:   envoyID,
				Status:     "skipped",
				ChunksNeed: totalChunks,
				SkipReason: reason,
			})
		}
	}

	// Envoys with pending assignments that haven't reported yet.
	for envoyID, assignments := range t.assignments {
		if seen[envoyID] {
			continue
		}
		for _, a := range assignments {
			if a.SHA256 == sha256 {
				result = append(result, EnvoySwarmStatus{
					Hostname:   envoyID,
					Status:     "pending",
					ChunksNeed: totalChunks,
				})
				break
			}
		}
	}

	return result
}

// Prune removes stale entries from the peer map.
func (t *PeerTracker) Prune() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	for sha, blobPeers := range t.peers {
		for id, ps := range blobPeers {
			if now.Sub(ps.LastSeen) > t.staleThreshold {
				delete(blobPeers, id)
			}
		}
		if len(blobPeers) == 0 {
			delete(t.peers, sha)
		}
	}
}

// RegisterSeed records an envoy-originated blob. Returns true if this is a
// new seed (caller should distribute), false if already known. Mutable fields
// (Retention, Target) are updated on existing seeds so operators can edit
// the .swarm manifest and have changes take effect on the next check-in.
func (t *PeerTracker) RegisterSeed(sha256 string, state SeedState) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.seeds[sha256]; ok {
		// Update mutable fields.
		existing.Retention = state.Retention
		existing.Target = state.Target
		t.seeds[sha256] = existing
		return false
	}
	t.seeds[sha256] = state
	return true
}

// ExpiredSeeds returns SHA256s of active (non-retired) seeds whose retention has elapsed.
func (t *PeerTracker) ExpiredSeeds() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := time.Now()
	var expired []string
	for sha256, s := range t.seeds {
		if s.Retired {
			continue
		}
		if s.Retention > 0 && now.After(s.DistributedAt.Add(s.Retention)) {
			expired = append(expired, sha256)
		}
	}
	return expired
}

// GetSeed returns a seed state and true if the SHA256 is a known seed.
func (t *PeerTracker) GetSeed(sha256 string) (SeedState, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.seeds[sha256]
	return s, ok
}

// ResolveSeedPrefix finds a unique seed by SHA256 prefix (min 6 chars).
func (t *PeerTracker) ResolveSeedPrefix(prefix string) (string, bool) {
	if !IsValidHashPrefix(prefix) {
		return "", false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	var match string
	for sha256 := range t.seeds {
		if strings.HasPrefix(sha256, prefix) {
			if match != "" {
				return "", false // ambiguous
			}
			match = sha256
		}
	}
	return match, match != ""
}

// ListSeeds returns active (non-retired) seeds.
func (t *PeerTracker) ListSeeds() map[string]SeedState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]SeedState, len(t.seeds))
	for k, v := range t.seeds {
		if !v.Retired {
			out[k] = v
		}
	}
	return out
}

// ListRetiredSeeds returns seeds that have been retired (past retention).
func (t *PeerTracker) ListRetiredSeeds() map[string]SeedState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]SeedState)
	for k, v := range t.seeds {
		if v.Retired {
			out[k] = v
		}
	}
	return out
}

// StatusSummary returns aggregate counts for a blob without the full envoy list.
func (t *PeerTracker) StatusSummary(sha256 string) (complete, downloading, pending, total int) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	seen := make(map[string]bool)
	if blobPeers := t.peers[sha256]; blobPeers != nil {
		for envoyID, ps := range blobPeers {
			seen[envoyID] = true
			if ps.Complete {
				complete++
			} else {
				downloading++
			}
		}
	}
	if blobSkipped := t.skipped[sha256]; blobSkipped != nil {
		for envoyID := range blobSkipped {
			if !seen[envoyID] {
				seen[envoyID] = true
			}
		}
	}
	for envoyID, assignments := range t.assignments {
		if seen[envoyID] {
			continue
		}
		for _, a := range assignments {
			if a.SHA256 == sha256 {
				pending++
				seen[envoyID] = true
				break
			}
		}
	}
	total = len(seen)
	return
}

// RetireSeed marks a seed as retired instead of deleting it, preserving history.
func (t *PeerTracker) RetireSeed(sha256 string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.seeds[sha256]; ok {
		s.Retired = true
		s.RetiredAt = time.Now()
		t.seeds[sha256] = s
	}
}

// RemoveSeed permanently deletes a seed entry (used for explicit admin deletion).
func (t *PeerTracker) RemoveSeed(sha256 string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.seeds, sha256)
}

// SetChunkSources records where an envoy downloaded its chunks from.
func (t *PeerTracker) SetChunkSources(envoyID, sha256 string, sources map[string]int, details []ChunkDetail) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if blobPeers := t.peers[sha256]; blobPeers != nil {
		if ps := blobPeers[envoyID]; ps != nil {
			ps.ChunkSources = sources
			ps.ChunkDetails = details
		}
	}
}

// SetBrokenSeeds replaces the broken seed list for an envoy (refreshed each check-in).
func (t *PeerTracker) SetBrokenSeeds(envoyID string, broken []BrokenSeed) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(broken) == 0 {
		delete(t.brokenSeeds, envoyID)
	} else {
		t.brokenSeeds[envoyID] = broken
	}
}

// AllBrokenSeeds returns a copy of all broken seeds keyed by envoy ID.
func (t *PeerTracker) AllBrokenSeeds() map[string][]BrokenSeed {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string][]BrokenSeed, len(t.brokenSeeds))
	for k, v := range t.brokenSeeds {
		cp := make([]BrokenSeed, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// ParseBlobsTrait parses the comma-separated swarm.blobs trait value into
// individual SHA256 hashes.
func ParseBlobsTrait(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	var hashes []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if IsValidHash(p) {
			hashes = append(hashes, p)
		}
	}
	return hashes
}
