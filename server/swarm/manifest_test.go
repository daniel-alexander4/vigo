// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package swarm

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// manifestTestSHA returns a 64-char hex string with the given prefix.
func manifestTestSHA(prefix string) string {
	return prefix + strings.Repeat("0", 64-len(prefix))
}

// TestRecordSeedFromAgent_RejectsUnknown is the central re-poisoning
// guard: a freshly-restarted server with an empty manifest must reject
// agent gossip for SHAs it has never authoritatively seen. Without this
// the 2026-04-24 incident reproduces (every cached agent seed gets
// re-created within seconds of restart).
func TestRecordSeedFromAgent_RejectsUnknown(t *testing.T) {
	store := NewManifestStore()
	sha := manifestTestSHA("a1b2c3")

	err := store.RecordSeedFromAgent(sha, "label.bin", "*", "7d", "envoy-1", 4096)
	if !errors.Is(err, ErrUnknownSeed) {
		t.Fatalf("expected ErrUnknownSeed, got %v", err)
	}
	if len(store.Entries()) != 0 {
		t.Fatalf("agent gossip created an entry on an empty manifest — re-poisoning guard failed: %+v", store.Entries())
	}
}

// TestRecordSeedFromAgent_RefreshesKnown is the legitimate path: when
// the server has already recorded an entry via the authoritative admin
// push, agents reporting the same SHA via swarm_seeds traits should
// refresh LastReSeededAt without churning UpdatedAt.
func TestRecordSeedFromAgent_RefreshesKnown(t *testing.T) {
	store := NewManifestStore()
	sha := manifestTestSHA("d4e5f6")

	// Admin push lands the entry first.
	store.RecordSeed(sha, "boot.iso", "*.web", "7d", "server", 1024)
	before, ok := lookup(store, sha)
	if !ok {
		t.Fatal("entry missing after admin RecordSeed")
	}

	// Sleep enough that the millisecond timestamps cannot collide.
	time.Sleep(2 * time.Millisecond)

	// Agent reports it's holding the same blob — should refresh, not error.
	if err := store.RecordSeedFromAgent(sha, "boot.iso", "*.web", "7d", "envoy-7", 1024); err != nil {
		t.Fatalf("expected nil for known SHA, got %v", err)
	}
	after, _ := lookup(store, sha)
	if after.LastReSeededAt <= before.LastReSeededAt {
		t.Errorf("LastReSeededAt should have advanced: before=%d after=%d", before.LastReSeededAt, after.LastReSeededAt)
	}
}

// TestRecordSeedFromAgent_RejectsRevoked verifies a revoked entry can't
// be resurrected by an agent's gossip — the operator's revoke wins.
func TestRecordSeedFromAgent_RejectsRevoked(t *testing.T) {
	store := NewManifestStore()
	sha := manifestTestSHA("777aaa")
	store.RecordSeed(sha, "old.bin", "*", "7d", "server", 512)
	store.Revoke(sha)

	if err := store.RecordSeedFromAgent(sha, "old.bin", "*", "7d", "envoy-3", 512); !errors.Is(err, ErrUnknownSeed) {
		t.Fatalf("expected ErrUnknownSeed for revoked entry, got %v", err)
	}
	if !store.IsRevoked(sha) {
		t.Error("entry must remain revoked after agent gossip attempt")
	}
}

// TestRecordSeed_AdminPathCreatesEntry verifies the unconditional
// creator path still works for the admin distribute endpoint after
// RecordSeedFromAgent gates the gossip path.
func TestRecordSeed_AdminPathCreatesEntry(t *testing.T) {
	store := NewManifestStore()
	sha := manifestTestSHA("11abcd")

	store.RecordSeed(sha, "app.tar.gz", "*.web", "7d", "server", 8192)
	e, ok := lookup(store, sha)
	if !ok {
		t.Fatal("admin RecordSeed must create the entry unconditionally")
	}
	if e.Status != "active" {
		t.Errorf("expected active, got %q", e.Status)
	}
	if e.LastReSeededAt == 0 {
		t.Error("expected LastReSeededAt to be set on creation")
	}
}

// TestStaleActive_PicksEntriesPastTTL exercises the predicate the
// liveness pruner walks on every tick.
func TestStaleActive_PicksEntriesPastTTL(t *testing.T) {
	store := NewManifestStore()
	freshSHA := manifestTestSHA("aaaaaa")
	staleSHA := manifestTestSHA("bbbbbb")

	store.RecordSeed(freshSHA, "fresh.bin", "*", "7d", "server", 1)
	store.RecordSeed(staleSHA, "stale.bin", "*", "7d", "server", 1)

	// Backdate stale entry's LastReSeededAt by hand so the test is
	// deterministic without sleeping for hours.
	mustEdit(t, store, staleSHA, func(e *ManifestEntry) {
		e.LastReSeededAt = uint64(time.Now().Add(-48 * time.Hour).UnixMilli())
	})

	stale := store.StaleActive(24 * time.Hour)
	if len(stale) != 1 || stale[0] != staleSHA {
		t.Fatalf("expected only stale SHA past 24h cutoff, got %v", stale)
	}
}

// TestStaleActive_TTLZeroDisables verifies a zero/negative TTL skips
// the sweep entirely.
func TestStaleActive_TTLZeroDisables(t *testing.T) {
	store := NewManifestStore()
	store.RecordSeed(manifestTestSHA("cccccc"), "x.bin", "*", "7d", "server", 1)
	if got := store.StaleActive(0); got != nil {
		t.Errorf("ttl=0 should disable; got %v", got)
	}
	if got := store.StaleActive(-1 * time.Hour); got != nil {
		t.Errorf("negative ttl should disable; got %v", got)
	}
}

// TestStaleActive_SkipsRevoked verifies revoked entries are not flagged
// (they're handled by PruneRevoked).
func TestStaleActive_SkipsRevoked(t *testing.T) {
	store := NewManifestStore()
	sha := manifestTestSHA("dddddd")
	store.RecordSeed(sha, "x.bin", "*", "7d", "server", 1)
	store.Revoke(sha)
	if got := store.StaleActive(1 * time.Nanosecond); len(got) != 0 {
		t.Errorf("revoked entries must not appear in StaleActive: %v", got)
	}
}

// TestPersistentManifest_Roundtrip pins the central correctness
// invariant: a server restart preserves the manifest. Writes via the
// debounced flusher must land on disk by the time Stop() returns.
func TestPersistentManifest_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	sha := manifestTestSHA("eeeeee")

	store, err := NewPersistentManifestStore(path, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewPersistentManifestStore: %v", err)
	}
	store.RecordSeed(sha, "kept.bin", "*", "7d", "server", 4096)
	store.Stop()

	store2, err := NewPersistentManifestStore(path, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer store2.Stop()
	e, ok := lookup(store2, sha)
	if !ok {
		t.Fatal("entry not preserved across restart")
	}
	if e.Label != "kept.bin" || e.Status != "active" {
		t.Errorf("entry corrupted across restart: %+v", e)
	}
	if e.LastReSeededAt == 0 {
		t.Error("LastReSeededAt should survive the roundtrip")
	}
}

// TestPersistentManifest_LegacyFileBackfillsLastReSeededAt verifies a
// pre-0.38.12 manifest file (entries lack LastReSeededAt) loads
// without flagging every entry as stale on the next pruner tick.
func TestPersistentManifest_LegacyFileBackfillsLastReSeededAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	sha := manifestTestSHA("ff0000")

	// Hand-write a legacy file (no last_re_seeded_at field).
	now := uint64(time.Now().UnixMilli())
	legacy := map[string]ManifestEntry{
		sha: {
			SHA256:    sha,
			Label:     "legacy.bin",
			SeederID:  "envoy-x",
			Status:    "active",
			UpdatedAt: now,
			UpdatedBy: "envoy-x",
		},
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}

	store, err := NewPersistentManifestStore(path, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("load legacy: %v", err)
	}
	defer store.Stop()
	e, _ := lookup(store, sha)
	if e.LastReSeededAt != now {
		t.Errorf("legacy entry should backfill LastReSeededAt from UpdatedAt: got LastReSeededAt=%d, UpdatedAt=%d", e.LastReSeededAt, now)
	}
}

// TestPersistentManifest_EmptyPathIsInMemory verifies the
// no-persistence fallback path used by tests.
func TestPersistentManifest_EmptyPathIsInMemory(t *testing.T) {
	store, err := NewPersistentManifestStore("", time.Second)
	if err != nil {
		t.Fatalf("empty path should be fine: %v", err)
	}
	store.RecordSeed(manifestTestSHA("123abc"), "m.bin", "*", "7d", "server", 1)
	store.Stop() // must be safe on in-memory store
	if len(store.Entries()) != 1 {
		t.Errorf("in-memory store lost entry on Stop")
	}
}

// lookup returns the entry for sha if present, or zero+false otherwise.
func lookup(m *ManifestStore, sha string) (ManifestEntry, bool) {
	for _, e := range m.Entries() {
		if e.SHA256 == sha {
			return e, true
		}
	}
	return ManifestEntry{}, false
}

// mustEdit applies fn to the entry under sha and writes it back. Used
// by tests that need to fast-forward LastReSeededAt without sleeping.
func mustEdit(t *testing.T, m *ManifestStore, sha string, fn func(*ManifestEntry)) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[sha]
	if !ok {
		t.Fatalf("entry %s missing", sha)
	}
	fn(&e)
	m.entries[sha] = e
}
