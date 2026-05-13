// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ManifestEntry is a single blob entry in the swarm manifest CRDT.
type ManifestEntry struct {
	SHA256    string `json:"sha256"`
	Label     string `json:"label"`
	Target    string `json:"target"`
	Retention string `json:"retention"`
	SeederID  string `json:"seeder_id"`
	Size      uint64 `json:"size"`
	Status    string `json:"status"`     // "active" or "revoked"
	UpdatedAt uint64 `json:"updated_at"` // Unix milliseconds
	UpdatedBy string `json:"updated_by"` // envoy ID or "server"
	// LastReSeededAt is the unix-ms timestamp of the most recent
	// RecordSeed call for this entry — *any* path that re-asserts the
	// entry is alive (server admin push, or an agent reporting it via
	// the swarm_seeds trait). Distinct from UpdatedAt, which holds LWW
	// semantics: LWW must change with revocation; LastReSeededAt
	// shouldn't, since it's "when did we last see a publisher claim
	// this is alive." Drives the liveness TTL sweep in
	// RetentionPruner: if no envoy has re-asserted the entry within
	// the TTL window, the pruner revokes it.
	//
	// Zero on entries persisted before 0.38.12 — load() backfills with
	// UpdatedAt so first-load entries don't get revoked en masse.
	LastReSeededAt uint64 `json:"last_re_seeded_at,omitempty"`
}

// ManifestStore is a server-side LWW-Map CRDT for swarm blob state.
// The server maintains this to relay manifest entries to envoys via
// check-in responses (fallback gossip channel for cross-subnet envoys).
//
// Mutations are persisted to a JSON file via a debounced background
// flusher (default 1s cadence) so a server restart preserves intent —
// without persistence, an empty post-restart manifest gets re-poisoned
// by agents reporting their cached `swarm_seeds` traits within seconds
// (root cause of the 2026-04-24 gitback reset incident, see
// `tshoot/log-archive-cairn-resolved-20260424-124050.md`). Agents can
// only refresh existing entries (`RecordSeedFromAgent`); creating new
// entries via gossip is rejected.
type ManifestStore struct {
	mu      sync.RWMutex
	entries map[string]ManifestEntry // sha256 → entry
	version uint64

	persistPath string       // empty = in-memory only (for tests)
	dirty       atomic.Bool  // mutation set; flusher clears
	stopCh      chan struct{}
	doneCh      chan struct{}
}

// NewManifestStore creates an empty in-memory manifest store. Suitable
// for tests; production callers should use NewPersistentManifestStore
// so server restarts don't lose intent.
func NewManifestStore() *ManifestStore {
	return &ManifestStore{
		entries: make(map[string]ManifestEntry),
	}
}

// NewPersistentManifestStore loads any existing manifest from
// `persistPath` and spawns a background flusher that writes mutations
// at most once per `flushInterval`. An empty `persistPath` falls back
// to in-memory (same as NewManifestStore). Atomic temp+rename writes;
// directory created if missing (mode 0755). File mode 0600 — manifest
// entries name internal envoy IDs and seed labels but no secrets, but
// 0600 keeps it tight and matches the policy for other server state.
func NewPersistentManifestStore(persistPath string, flushInterval time.Duration) (*ManifestStore, error) {
	m := &ManifestStore{
		entries:     make(map[string]ManifestEntry),
		persistPath: persistPath,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
	if persistPath == "" {
		close(m.doneCh)
		return m, nil
	}
	if err := os.MkdirAll(filepath.Dir(persistPath), 0755); err != nil {
		return nil, fmt.Errorf("creating manifest dir: %w", err)
	}
	if err := m.load(); err != nil {
		return nil, fmt.Errorf("loading manifest from %s: %w", persistPath, err)
	}
	if flushInterval <= 0 {
		flushInterval = time.Second
	}
	go m.flushLoop(flushInterval)
	return m, nil
}

// Stop signals the flusher to drain and exit. Performs a final flush
// so a graceful shutdown never loses the most recent mutations. Safe
// to call multiple times; safe on an in-memory store.
func (m *ManifestStore) Stop() {
	if m.stopCh == nil {
		return
	}
	select {
	case <-m.stopCh:
		// Already stopped.
	default:
		close(m.stopCh)
	}
	<-m.doneCh
}

func (m *ManifestStore) flushLoop(interval time.Duration) {
	defer close(m.doneCh)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			// Drain any final mutation before exiting.
			if m.dirty.CompareAndSwap(true, false) {
				if err := m.persist(); err != nil {
					slog.Warn("manifest: final flush failed", "error", err)
				}
			}
			return
		case <-ticker.C:
			if m.dirty.CompareAndSwap(true, false) {
				if err := m.persist(); err != nil {
					slog.Warn("manifest: flush failed", "error", err)
					// Re-mark dirty so next tick retries; otherwise a
					// transient I/O error would silently lose the
					// mutation.
					m.dirty.Store(true)
				}
			}
		}
	}
}

// persist serializes the current entries to persistPath via temp+rename.
// Caller must NOT hold m.mu — persist takes RLock for the snapshot.
func (m *ManifestStore) persist() error {
	if m.persistPath == "" {
		return nil
	}
	m.mu.RLock()
	snapshot := make(map[string]ManifestEntry, len(m.entries))
	for k, v := range m.entries {
		snapshot[k] = v
	}
	m.mu.RUnlock()

	body, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := m.persistPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, m.persistPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// load reads persistPath if present and populates entries. Missing file
// is fine (fresh deploy). Backfills LastReSeededAt from UpdatedAt for
// pre-0.38.12 entries — without the backfill the next pruner tick
// would revoke every legacy entry as "stale."
func (m *ManifestStore) load() error {
	body, err := os.ReadFile(m.persistPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(body) == 0 {
		return nil
	}
	var loaded map[string]ManifestEntry
	if err := json.Unmarshal(body, &loaded); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	for sha, e := range loaded {
		if e.LastReSeededAt == 0 {
			e.LastReSeededAt = e.UpdatedAt
			loaded[sha] = e
		}
	}
	m.mu.Lock()
	m.entries = loaded
	m.mu.Unlock()
	return nil
}

func (m *ManifestStore) markDirty() {
	if m.persistPath != "" {
		m.dirty.Store(true)
	}
}

// Revoke creates a revocation entry for a blob, signed by "server".
func (m *ManifestStore) Revoke(sha256 string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := uint64(time.Now().UnixMilli())

	m.entries[sha256] = ManifestEntry{
		SHA256:    sha256,
		Status:    "revoked",
		UpdatedAt: now,
		UpdatedBy: "server",
	}
	m.version++
	m.markDirty()
}

// RecordSeed records an active seed in the manifest. Overwrites prior
// revocations so re-distributing a deleted blob works correctly.
// `LastReSeededAt` is bumped on every call regardless of whether the
// entry is otherwise idempotent — that's the signal the liveness
// pruner uses to decide an entry is still actively held.
//
// This method is the **creator path**: callers may introduce new
// entries unconditionally. Used by the admin filecast-distribute API
// (server-side, authoritative). For agent gossip, use
// RecordSeedFromAgent so unknown SHAs are rejected.
func (m *ManifestStore) RecordSeed(sha256, label, target, retention, seederID string, size uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordSeedLocked(sha256, label, target, retention, seederID, size)
}

func (m *ManifestStore) recordSeedLocked(sha256, label, target, retention, seederID string, size uint64) {
	now := uint64(time.Now().UnixMilli())

	existing, exists := m.entries[sha256]
	// If already active with the same seeder, bump LastReSeededAt only
	// — the rest of the entry is unchanged so we don't churn UpdatedAt
	// (UpdatedAt drives LWW; LastReSeededAt is independent).
	if exists && existing.Status == "active" && existing.SeederID == seederID {
		existing.LastReSeededAt = now
		m.entries[sha256] = existing
		m.markDirty()
		return
	}
	m.entries[sha256] = ManifestEntry{
		SHA256:         sha256,
		Label:          label,
		Target:         target,
		Retention:      retention,
		SeederID:       seederID,
		Size:           size,
		Status:         "active",
		UpdatedAt:      now,
		UpdatedBy:      seederID,
		LastReSeededAt: now,
	}
	m.version++
	m.markDirty()
}

// ErrUnknownSeed is returned by RecordSeedFromAgent when an agent
// gossips a SHA the server has never seen via the authoritative
// (admin push) path. The agent's report is dropped; the entry is not
// created. Callers should log + skip, not retry.
var ErrUnknownSeed = errors.New("manifest: unknown seed (agent cannot create entries)")

// RecordSeedFromAgent refreshes an entry's LastReSeededAt when an agent
// reports it via swarm_seeds traits, but **does not** create new
// entries. This closes the re-poisoning footgun: a freshly-restarted
// server (or one whose manifest has been wiped) will not be re-
// populated by stale agent gossip; the only path that introduces a
// new entry is the authoritative server-side admin push. See
// `tshoot/log-archive-cairn-resolved-20260424-124050.md` for the
// incident this guards.
//
// Returns ErrUnknownSeed when no entry is present, or nil on success.
// On a present-but-revoked entry: also rejected — once revoked, an
// agent's gossip cannot resurrect the entry (operator must
// re-distribute via admin push).
func (m *ManifestStore) RecordSeedFromAgent(sha256, label, target, retention, seederID string, size uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, exists := m.entries[sha256]
	if !exists || existing.Status != "active" {
		return ErrUnknownSeed
	}
	m.recordSeedLocked(sha256, label, target, retention, seederID, size)
	return nil
}

// IsRevoked returns true if the blob is revoked in the manifest.
func (m *ManifestStore) IsRevoked(sha256 string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[sha256]
	return ok && e.Status == "revoked"
}

// Entries returns all manifest entries (snapshot).
// Get returns the manifest entry for a sha256 if present, plus a bool
// indicating whether the entry was found. Used by the agent-trait
// re-registration path so post-restart `tracker.seeds` rehydration can
// reuse the manifest's persisted timestamps instead of `time.Now()` —
// otherwise non-seeder envoys' PeerState.FirstSeen (set earlier in the
// same CheckIn handler by `ingestSwarmTraits`) lands before the seed's
// fresh DistributedAt and flips the envoys to "cached" indefinitely.
func (m *ManifestStore) Get(sha256 string) (ManifestEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[sha256]
	return e, ok
}

func (m *ManifestStore) Entries() []ManifestEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ManifestEntry, 0, len(m.entries))
	for _, e := range m.entries {
		result = append(result, e)
	}
	return result
}

// RevokedEntries returns only revoked entries (for check-in responses).
func (m *ManifestStore) RevokedEntries() []ManifestEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []ManifestEntry
	for _, e := range m.entries {
		if e.Status == "revoked" {
			result = append(result, e)
		}
	}
	return result
}

// Merge applies a remote manifest entry using LWW semantics. Reserved
// for future cross-server gossip (currently unused — agents talk to
// the server, not to each other through the manifest). Kept for
// proto-level forward compat; callers that received this from an
// untrusted source MUST validate first.
func (m *ManifestStore) Merge(entry ManifestEntry) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.entries[entry.SHA256]
	if !ok {
		m.entries[entry.SHA256] = entry
		m.version++
		m.markDirty()
		return true
	}

	if entryWins(&entry, &existing) {
		m.entries[entry.SHA256] = entry
		m.version++
		m.markDirty()
		return true
	}
	return false
}

// Version returns the current manifest version.
func (m *ManifestStore) Version() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.version
}

// PruneRevoked removes revoked entries older than maxAge.
func (m *ManifestStore) PruneRevoked(maxAge time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := uint64(time.Now().Add(-maxAge).UnixMilli())
	pruned := 0
	for sha, e := range m.entries {
		if e.Status == "revoked" && e.UpdatedAt < cutoff {
			delete(m.entries, sha)
			pruned++
		}
	}
	if pruned > 0 {
		m.version++
		m.markDirty()
	}
	return pruned
}

// StaleActive returns SHAs of active entries whose LastReSeededAt is
// older than `now - ttl`. The pruner walks this list and calls Revoke
// on each — done outside the lock so the revocation gossip path can
// fire without contending with check-in mutations.
func (m *ManifestStore) StaleActive(ttl time.Duration) []string {
	if ttl <= 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	cutoff := uint64(time.Now().Add(-ttl).UnixMilli())
	var stale []string
	for sha, e := range m.entries {
		if e.Status != "active" {
			continue
		}
		// Defensive: an entry with zero LastReSeededAt slipped through
		// load() backfill (shouldn't happen, but the in-RAM path can
		// still produce zero in tests). Use UpdatedAt as a fallback so
		// a buggy zero doesn't trigger immediate revocation.
		ts := e.LastReSeededAt
		if ts == 0 {
			ts = e.UpdatedAt
		}
		if ts < cutoff {
			stale = append(stale, sha)
		}
	}
	return stale
}

// ResolvePrefix finds a unique entry by SHA256 prefix (min 6 chars).
func (m *ManifestStore) ResolvePrefix(prefix string) (string, bool) {
	if !IsValidHashPrefix(prefix) {
		return "", false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var match string
	for sha256 := range m.entries {
		if strings.HasPrefix(sha256, prefix) {
			if match != "" {
				return "", false // ambiguous
			}
			match = sha256
		}
	}
	return match, match != ""
}

// entryWins returns true if a should replace b (LWW with revoke-wins).
func entryWins(a, b *ManifestEntry) bool {
	if a.UpdatedAt != b.UpdatedAt {
		return a.UpdatedAt > b.UpdatedAt
	}
	// Tie: revoked wins.
	if a.Status != b.Status {
		return a.Status == "revoked"
	}
	// Same status, same timestamp: higher author wins.
	return a.UpdatedBy > b.UpdatedBy
}
