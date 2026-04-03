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
	Address  string    // ip:port (e.g., "10.0.1.5:1531")
	Chunks   []uint32  // chunk indices (nil = all chunks / complete)
	Complete bool      // true if the envoy has the full blob
	LastSeen time.Time // last time this envoy reported
}

// AssignmentState tracks a pending blob assignment for an envoy.
type AssignmentState struct {
	SHA256 string
	Size   uint64
	Label  string
}

// EnvoySwarmStatus is the per-envoy status returned by the status API.
type EnvoySwarmStatus struct {
	Hostname   string `json:"hostname"`
	Status     string `json:"status"`      // "complete", "downloading", "pending", "skipped"
	ChunksHave int    `json:"chunks_have"` // number of chunks downloaded
	ChunksNeed int    `json:"chunks_need"` // total chunks in the blob
	SkipReason string `json:"skip_reason,omitempty"`
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
	blobPeers[envoyID] = &PeerState{
		Address:  address,
		Chunks:   chunks,
		Complete: complete,
		LastSeen: time.Now(),
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
func (t *PeerTracker) Status(sha256 string, totalChunks int) []EnvoySwarmStatus {
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
			if ps.Complete {
				st = "complete"
				have = totalChunks
			}
			result = append(result, EnvoySwarmStatus{
				Hostname:   envoyID, // will be resolved to hostname by the API
				Status:     st,
				ChunksHave: have,
				ChunksNeed: totalChunks,
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
