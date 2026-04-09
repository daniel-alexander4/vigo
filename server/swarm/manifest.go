// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"strings"
	"sync"
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
}

// ManifestStore is a server-side LWW-Map CRDT for swarm blob state.
// The server maintains this to relay manifest entries to envoys via
// check-in responses (fallback gossip channel for cross-subnet envoys).
type ManifestStore struct {
	mu      sync.RWMutex
	entries map[string]ManifestEntry // sha256 → entry
	version uint64
}

// NewManifestStore creates an empty manifest store.
func NewManifestStore() *ManifestStore {
	return &ManifestStore{
		entries: make(map[string]ManifestEntry),
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
}

// RecordSeed records an active seed in the manifest. Overwrites prior
// revocations so re-distributing a deleted blob works correctly.
func (m *ManifestStore) RecordSeed(sha256, label, target, retention, seederID string, size uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := uint64(time.Now().UnixMilli())

	// If already active with the same seeder, skip (idempotent).
	if existing, ok := m.entries[sha256]; ok && existing.Status == "active" && existing.SeederID == seederID {
		return
	}
	m.entries[sha256] = ManifestEntry{
		SHA256:    sha256,
		Label:     label,
		Target:    target,
		Retention: retention,
		SeederID:  seederID,
		Size:      size,
		Status:    "active",
		UpdatedAt: now,
		UpdatedBy: seederID,
	}
	m.version++
}

// IsRevoked returns true if the blob is revoked in the manifest.
func (m *ManifestStore) IsRevoked(sha256 string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[sha256]
	return ok && e.Status == "revoked"
}

// Entries returns all manifest entries (snapshot).
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

// Merge applies a remote manifest entry using LWW semantics.
func (m *ManifestStore) Merge(entry ManifestEntry) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.entries[entry.SHA256]
	if !ok {
		m.entries[entry.SHA256] = entry
		m.version++
		return true
	}

	if entryWins(&entry, &existing) {
		m.entries[entry.SHA256] = entry
		m.version++
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
	}
	return pruned
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
