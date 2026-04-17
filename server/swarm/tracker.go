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
	Address          string         // ip:port (e.g., "10.0.1.5:1531")
	Chunks           []uint32       // chunk indices (nil = all chunks / complete)
	Complete         bool           // true if the envoy has the full blob
	FirstSeen        time.Time      // when this envoy first reported for this blob
	LastSeen         time.Time      // last time this envoy reported
	ChunkSources     map[string]int // source address → chunk count (set when complete)
	ChunkDetails     []ChunkDetail  // per-chunk source and timing
	ActiveDownloadMs int64          // scheduler wall-clock ms for this run; excludes chunks already on disk before the run started
	Evicted          bool           // envoy reported (via absence from swarm.blobs) that the blob is gone from its cache after a revoke
	EvictedAt        time.Time      // when the server detected the eviction
}

// ChunkDetail records how a single chunk was downloaded.
type ChunkDetail struct {
	Chunk      int    `json:"chunk"`
	Source     string `json:"source"`
	DurationMs int    `json:"ms"`
}

// EnvoySwarmStatus is the per-envoy status returned by the status API.
//
// Status values:
//   - "source" — this envoy is the original seeder; has the blob by
//     construction. Distinct from "complete" so operators can tell
//     seeders apart from downloaders at a glance.
//   - "complete" — envoy downloaded every chunk from peers.
//   - "downloading" — envoy has a partial chunk set and is still fetching.
//   - "pending" — envoy matches the target pattern but hasn't reported
//     any chunks yet (synthesized by the API handler's fan-out).
//   - "skipped" — envoy reported skipping the blob (e.g. authorization).
//   - "revoking" — seed was revoked but this envoy still has the blob locally.
//   - "revoked"  — seed was revoked and this envoy has confirmed eviction.
//
// There is no "is_source" boolean — the seeder is identified entirely by
// Status == "source". Keeping a single source-of-truth field prevents
// the two from ever disagreeing.
type EnvoySwarmStatus struct {
	Hostname         string         `json:"hostname"`
	Status           string         `json:"status"`
	ChunksHave       int            `json:"chunks_have"` // number of chunks downloaded
	ChunksNeed       int            `json:"chunks_need"` // total chunks in the blob
	SkipReason       string         `json:"skip_reason,omitempty"`
	StartedAt        time.Time      `json:"started_at,omitempty"`         // when the server first saw this envoy for this blob
	CompletedAt      time.Time      `json:"completed_at,omitempty"`       // when the server saw completion (not download duration)
	ActiveDownloadMs int64          `json:"active_download_ms,omitempty"` // scheduler wall-clock ms; excludes chunks that were already cached before the download run began
	ChunkSources     map[string]int `json:"chunk_sources,omitempty"`      // source address → chunk count
	ChunkDetails     []ChunkDetail  `json:"chunk_details,omitempty"`      // per-chunk source and timing
}

// SeedState tracks an envoy-originated blob that was auto-distributed.
type SeedState struct {
	Label         string
	Size          uint64
	Target        string
	Retention     time.Duration
	SeederID      string
	DistributedAt time.Time
	Revoked       bool
	RevokedAt     time.Time
}

// BrokenSeed represents a seed file with an invalid or missing manifest.
type BrokenSeed struct {
	Filename string `json:"filename"`
	Error    string `json:"error"`
}

// PeerTracker maintains an in-memory map of which envoys have which blobs
// (or chunks thereof). Agents self-assign via the manifest CRDT, so the
// tracker no longer manages per-envoy assignments. It remains for status
// reporting and seed lifecycle.
type PeerTracker struct {
	mu sync.RWMutex

	// peers maps sha256 → envoyID → PeerState.
	peers map[string]map[string]*PeerState

	// skipped maps sha256 → envoyID → reason.
	skipped map[string]map[string]string

	// seeds maps sha256 → SeedState for envoy-originated blobs.
	seeds map[string]SeedState

	// brokenSeeds maps envoyID → list of broken seed files (refreshed each check-in).
	brokenSeeds map[string][]BrokenSeed

	// rejectedSeeds maps envoyID → []sha256 for seeds rejected by authorization policy.
	rejectedSeeds map[string][]string

	// staleThreshold is how long before a peer entry expires.
	staleThreshold time.Duration
}

// NewPeerTracker creates a peer tracker. staleThreshold is typically 2× the
// base check-in interval.
func NewPeerTracker(staleThreshold time.Duration) *PeerTracker {
	return &PeerTracker{
		peers:          make(map[string]map[string]*PeerState),
		skipped:        make(map[string]map[string]string),
		seeds:          make(map[string]SeedState),
		brokenSeeds:    make(map[string][]BrokenSeed),
		rejectedSeeds:  make(map[string][]string),
		staleThreshold: staleThreshold,
	}
}

// SetRejectedSeeds records seed hashes rejected for a given envoy.
// Replaced on each check-in (same lifecycle as brokenSeeds).
func (t *PeerTracker) SetRejectedSeeds(envoyID string, hashes []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rejectedSeeds[envoyID] = hashes
}

// PopRejectedSeeds returns and clears rejected seed hashes for the envoy.
// Deliver-once: if the agent misses the message, the next check-in
// re-reports the seed, the server re-rejects, and the cycle self-heals.
func (t *PeerTracker) PopRejectedSeeds(envoyID string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	hashes := t.rejectedSeeds[envoyID]
	delete(t.rejectedSeeds, envoyID)
	return hashes
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
		// Update existing — preserve FirstSeen, ChunkSources, and ActiveDownloadMs.
		existing.Address = address
		existing.Chunks = chunks
		existing.Complete = complete
		existing.LastSeen = now
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

// CompletePeerCount returns the number of envoys that have the complete blob,
// excluding the given envoy ID (pass "" to count all).
func (t *PeerTracker) CompletePeerCount(sha256 string, excludeEnvoyID string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	count := 0
	if blobPeers := t.peers[sha256]; blobPeers != nil {
		for id, ps := range blobPeers {
			if id == excludeEnvoyID {
				continue
			}
			if ps.Complete {
				count++
			}
		}
	}
	return count
}

// Status returns per-envoy status for a blob. Used by the status API.
// Status is derived from trait reports (swarm.blobs) — agents self-assign
// via the manifest CRDT, so there is no "pending" state on the server.
//
// When the seed is revoked, complete/downloading statuses map to "revoking"
// (envoy still has the blob locally) or "revoked" (envoy has reported via
// trait absence that the blob is gone). Skipped entries are dropped for
// revoked seeds — there's nothing useful for the operator to see from an
// envoy that never had the blob in the first place.
func (t *PeerTracker) Status(sha256 string, totalChunks int, seederID string) []EnvoySwarmStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	seedRevoked := false
	var seedDistributedAt time.Time
	if seed, ok := t.seeds[sha256]; ok {
		seedDistributedAt = seed.DistributedAt
		if seed.Revoked {
			seedRevoked = true
		}
	}

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
				// If this envoy was already tracking the blob before
				// the current distribution started, it had the blob
				// cached — no download occurred for this round.
				if !seedDistributedAt.IsZero() && ps.FirstSeen.Before(seedDistributedAt) {
					st = "cached"
				}
				have = totalChunks
				completedAt = ps.LastSeen
			}
			// The seeder has every chunk by construction; override the
			// complete/downloading label with "source" so it's visually
			// distinct. Revoked seeds are left alone — a revoked seeder
			// follows the same revoking/revoked flow as every other peer.
			if !seedRevoked && envoyID == seederID {
				st = "source"
				have = totalChunks
			}
			if seedRevoked {
				if ps.Evicted {
					st = "revoked"
				} else {
					st = "revoking"
				}
			}
			dlMs := ps.ActiveDownloadMs
			// Fallback for agents that pre-date active_download_ms reporting:
			// sum per-chunk durations (overstates for parallel downloads
			// but better than showing nothing).
			if dlMs == 0 && len(ps.ChunkDetails) > 0 {
				for _, cd := range ps.ChunkDetails {
					dlMs += int64(cd.DurationMs)
				}
			}
			result = append(result, EnvoySwarmStatus{
				Hostname:         envoyID,
				Status:           st,
				ChunksHave:       have,
				ChunksNeed:       totalChunks,
				StartedAt:        ps.FirstSeen,
				CompletedAt:      completedAt,
				ActiveDownloadMs: dlMs,
				ChunkSources:     ps.ChunkSources,
				ChunkDetails:     ps.ChunkDetails,
			})
		}
	}

	// The seeder does not download its own blob and therefore will
	// usually not appear in peers[]. Synthesize an entry for it so the
	// status view shows "★ <seeder> source" instead of silently
	// dropping it into the API handler's pending fan-out. Only done
	// for active seeds — on revoked seeds we don't invent state the
	// tracker hasn't observed.
	if seederID != "" && !seedRevoked && !seen[seederID] {
		seen[seederID] = true
		result = append(result, EnvoySwarmStatus{
			Hostname:   seederID,
			Status:     "source",
			ChunksHave: totalChunks,
			ChunksNeed: totalChunks,
		})
	}

	// Envoys that skipped. Omitted for revoked seeds — a "skipped" row
	// represents an envoy that never downloaded the blob, so there's
	// nothing to evict and nothing to surface after a revoke.
	if !seedRevoked {
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
	}

	return result
}

// MarkEvicted records that an envoy has reported (via absence of the hash
// from its swarm.blobs trait) that it no longer has the blob locally. Only
// meaningful for revoked seeds — the operator can see which envoys have
// finished eviction and which are still holding the blob. No-op if the
// envoy has no record for the hash.
func (t *PeerTracker) MarkEvicted(envoyID, sha256 string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	blobPeers := t.peers[sha256]
	if blobPeers == nil {
		return
	}
	ps := blobPeers[envoyID]
	if ps == nil || ps.Evicted {
		return
	}
	ps.Evicted = true
	ps.EvictedAt = time.Now()
}

// RevokedSeeds returns the sha256 of every seed currently flagged as
// revoked in the tracker. Used by ingestSwarmTraits to decide which
// peer records to mark as evicted when an envoy's latest trait report
// omits a hash it used to have.
func (t *PeerTracker) RevokedSeeds() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []string
	for sha, seed := range t.seeds {
		if seed.Revoked {
			out = append(out, sha)
		}
	}
	return out
}

// HasPeerRecord returns true if the tracker has a per-envoy record for
// the given (envoyID, sha256). Used to decide whether a missing hash in
// a trait report represents a fresh eviction.
func (t *PeerTracker) HasPeerRecord(envoyID, sha256 string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	blobPeers := t.peers[sha256]
	if blobPeers == nil {
		return false
	}
	return blobPeers[envoyID] != nil
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
//
// Revoked seeds are re-activated when re-registered. This handles the
// revoke-then-redistribute case: the admin revokes a seed, then distributes
// the same content again. The new distribute places a fresh file in the
// seed directory, the trait collector reports it, and RegisterSeed
// re-activates the seed with a fresh DistributedAt timestamp.
func (t *PeerTracker) RegisterSeed(sha256 string, state SeedState) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.seeds[sha256]; ok {
		if existing.Revoked {
			// Re-activate: admin re-distributed a previously revoked seed.
			// Clear stale peer records from the pre-revoke round — those
			// envoys graveyarded the blob and are about to re-download, but
			// the old PeerState.Complete=true entries linger until fresh
			// trait reports arrive (~one cycle). Without this, Status()
			// reports every envoy as "complete" for one cycle after
			// re-distribute, which is misleading and confusing.
			delete(t.peers, sha256)
			delete(t.skipped, sha256)
			state.DistributedAt = time.Now()
			t.seeds[sha256] = state
			return true
		}
		// Update mutable fields. DistributedAt is NOT updated here —
		// this path fires on every trait re-registration (every check-in
		// cycle), which would push DistributedAt past envoys' FirstSeen
		// and make fresh downloads appear as cache hits. Only the
		// Distribute handler updates DistributedAt via TouchDistributedAt.
		existing.Retention = state.Retention
		existing.Target = state.Target
		t.seeds[sha256] = existing
		return false
	}
	t.seeds[sha256] = state
	return true
}

// TouchDistributedAt updates the seed's DistributedAt to now. Called by the
// Distribute handler on a non-revoked re-distribute so that Status() can
// detect cache hits (envoys whose FirstSeen predates DistributedAt). This
// is separate from RegisterSeed because RegisterSeed is also called by the
// trait re-registration path on every check-in cycle — updating
// DistributedAt there would push it past envoys' FirstSeen and make fresh
// downloads appear as cached.
func (t *PeerTracker) TouchDistributedAt(sha256 string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if seed, ok := t.seeds[sha256]; ok {
		seed.DistributedAt = time.Now()
		t.seeds[sha256] = seed
	}
}

// ExpiredSeeds returns SHA256s of active (non-revoked) seeds whose retention has elapsed.
func (t *PeerTracker) ExpiredSeeds() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := time.Now()
	var expired []string
	for sha256, s := range t.seeds {
		if s.Revoked {
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

// ListSeeds returns active (non-revoked) seeds.
func (t *PeerTracker) ListSeeds() map[string]SeedState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]SeedState, len(t.seeds))
	for k, v := range t.seeds {
		if !v.Revoked {
			out[k] = v
		}
	}
	return out
}

// ListRevokedSeeds returns seeds that have been revoked (admin action or
// retention expiry). Kept in tracker history for status reporting.
func (t *PeerTracker) ListRevokedSeeds() map[string]SeedState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]SeedState)
	for k, v := range t.seeds {
		if v.Revoked {
			out[k] = v
		}
	}
	return out
}

// StatusSummary returns aggregate counts for a blob without the full envoy list.
// Agents self-assign, so there is no "pending" count — only source, complete,
// downloading, and skipped. The seeder is counted in "source" regardless of
// whether it has reported chunks via peer tracking; synthesizing a source
// count for an otherwise-unreported seeder is what keeps list/dashboard totals
// honest (the seeder has the blob by construction).
func (t *PeerTracker) StatusSummary(sha256 string) (complete, downloading, pending, skipped, source, cached, total int) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// Look up the seederID and DistributedAt from the seed registration.
	seederID := ""
	var seedDistributedAt time.Time
	if seed, ok := t.seeds[sha256]; ok && !seed.Revoked {
		seederID = seed.SeederID
		seedDistributedAt = seed.DistributedAt
	}

	seen := make(map[string]bool)
	if blobPeers := t.peers[sha256]; blobPeers != nil {
		for envoyID, ps := range blobPeers {
			seen[envoyID] = true
			switch {
			case seederID != "" && envoyID == seederID:
				source++
			case ps.Complete && !seedDistributedAt.IsZero() && ps.FirstSeen.Before(seedDistributedAt):
				cached++
			case ps.Complete:
				complete++
			default:
				downloading++
			}
		}
	}
	// Seeder hasn't reported chunks (the common case — a seeder never
	// downloads its own blob). Count it anyway so the total reflects
	// reality.
	if seederID != "" && !seen[seederID] {
		seen[seederID] = true
		source++
	}
	if blobSkipped := t.skipped[sha256]; blobSkipped != nil {
		for envoyID := range blobSkipped {
			if !seen[envoyID] {
				seen[envoyID] = true
				skipped++
			}
		}
	}
	total = len(seen) - source
	return
}

// FindSeedByLabel returns the SHA256 of an active seed with the given label.
// Returns empty string if no active seed has that label.
func (t *PeerTracker) FindSeedByLabel(label string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for sha256, s := range t.seeds {
		if !s.Revoked && s.Label == label {
			return sha256
		}
	}
	return ""
}

// SeedsForEnvoy returns SHA256 hashes of active seeds where the given envoy
// is the seeder. Used to detect unseeded blobs.
func (t *PeerTracker) SeedsForEnvoy(envoyID string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []string
	for sha256, s := range t.seeds {
		if !s.Revoked && s.SeederID == envoyID {
			out = append(out, sha256)
		}
	}
	return out
}

// RevokeSeed marks a seed as revoked, preserving history. Operator-initiated
// (admin DELETE) and system-initiated (retention expiry, label collision,
// seeder stopped reporting) revocations all flow through here. Callers must
// also call ManifestStore.Revoke so the revocation gossips fleet-wide via
// the manifest CRDT.
func (t *PeerTracker) RevokeSeed(sha256 string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.seeds[sha256]; ok {
		s.Revoked = true
		s.RevokedAt = time.Now()
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
// activeDownloadMs is the scheduler's wall-clock for the run that produced
// these sources — it excludes chunks that were already cached before the run.
func (t *PeerTracker) SetChunkSources(envoyID, sha256 string, sources map[string]int, details []ChunkDetail, activeDownloadMs int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if blobPeers := t.peers[sha256]; blobPeers != nil {
		if ps := blobPeers[envoyID]; ps != nil {
			ps.ChunkSources = sources
			ps.ChunkDetails = details
			ps.ActiveDownloadMs = activeDownloadMs
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
