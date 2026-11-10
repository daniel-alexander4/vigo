// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package swarm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// pruneTestSHA returns a 64-char hex string with the given prefix.
func pruneTestSHA(prefix string) string {
	return prefix + strings.Repeat("0", 64-len(prefix))
}

// TestRetentionPruner_RevokesAndGossips verifies that an expired seed is
// both revoked in the tracker AND recorded in the manifest CRDT, so the
// revocation gossips fleet-wide via the next check-in cycle. Without the
// manifest write, retention-expired blobs would only be cleaned up on the
// original seeder via a now-removed sidechannel — consumers would keep
// stale cached copies forever. Regression guard for the 0.19.41 fix.
func TestRetentionPruner_RevokesAndGossips(t *testing.T) {
	tracker := NewPeerTracker(60 * time.Second)
	store := NewManifestStore()

	sha := pruneTestSHA("a1b2c3")

	// Register a seed whose retention has already elapsed.
	tracker.RegisterSeed(sha, SeedState{
		Label:         "expired.bin",
		SeederID:      "envoy-1",
		Retention:     1 * time.Millisecond,
		DistributedAt: time.Now().Add(-1 * time.Minute),
	})

	pruner := NewRetentionPruner(7 * 24 * time.Hour)
	pruner.SetTracker(tracker)
	pruner.SetManifestStore(store)
	pruner.Prune(context.Background())

	// Tracker side: seed must be marked revoked.
	seed, ok := tracker.GetSeed(sha)
	if !ok {
		t.Fatal("seed missing from tracker after prune")
	}
	if !seed.Revoked {
		t.Error("expected seed.Revoked=true after prune")
	}
	if seed.RevokedAt.IsZero() {
		t.Error("expected RevokedAt to be set")
	}

	// Manifest side: revocation must be recorded so it gossips fleet-wide.
	if !store.IsRevoked(sha) {
		t.Error("expected manifest store to record revocation — without this, " +
			"retention-expired blobs never gossip and consumer envoys keep them cached forever")
	}
}

// TestRetentionPruner_NoManifestStore verifies the pruner is safe when
// no manifest store is wired (defensive — main.go is supposed to wire it,
// but other call sites or tests may not).
func TestRetentionPruner_NoManifestStore(t *testing.T) {
	tracker := NewPeerTracker(60 * time.Second)
	sha := pruneTestSHA("d4e5f6")

	tracker.RegisterSeed(sha, SeedState{
		Label:         "expired.bin",
		SeederID:      "envoy-1",
		Retention:     1 * time.Millisecond,
		DistributedAt: time.Now().Add(-1 * time.Minute),
	})

	pruner := NewRetentionPruner(7 * 24 * time.Hour)
	pruner.SetTracker(tracker)
	// No SetManifestStore — must not panic.
	pruner.Prune(context.Background())

	seed, _ := tracker.GetSeed(sha)
	if !seed.Revoked {
		t.Error("expected seed to be revoked even without manifest store")
	}
}

// TestRetentionPruner_SkipsActiveSeeds verifies seeds whose retention
// has not yet elapsed are left alone.
func TestRetentionPruner_SkipsActiveSeeds(t *testing.T) {
	tracker := NewPeerTracker(60 * time.Second)
	store := NewManifestStore()
	sha := pruneTestSHA("999999")

	tracker.RegisterSeed(sha, SeedState{
		Label:         "fresh.bin",
		SeederID:      "envoy-1",
		Retention:     1 * time.Hour,
		DistributedAt: time.Now(),
	})

	pruner := NewRetentionPruner(7 * 24 * time.Hour)
	pruner.SetTracker(tracker)
	pruner.SetManifestStore(store)
	pruner.Prune(context.Background())

	seed, _ := tracker.GetSeed(sha)
	if seed.Revoked {
		t.Error("active seed should not be revoked by pruner")
	}
	if store.IsRevoked(sha) {
		t.Error("active seed should not be revoked in manifest")
	}
}

// TestRetentionPruner_LivenessSweepRevokesStale verifies the 0.38.12
// liveness guard: an active manifest entry whose LastReSeededAt has
// aged past livenessTTL is revoked + gossiped on the next prune tick.
// This is the safety net for entries whose last seeder permanently
// disappears — combined with RecordSeedFromAgent's gating, it keeps
// the manifest from accumulating orphaned entries forever.
func TestRetentionPruner_LivenessSweepRevokesStale(t *testing.T) {
	tracker := NewPeerTracker(60 * time.Second)
	store := NewManifestStore()
	freshSHA := pruneTestSHA("fff111")
	staleSHA := pruneTestSHA("fff222")

	store.RecordSeed(freshSHA, "fresh.bin", "*", "7d", "server", 1)
	store.RecordSeed(staleSHA, "stale.bin", "*", "7d", "server", 1)

	// Backdate the stale entry's LastReSeededAt.
	store.mu.Lock()
	e := store.entries[staleSHA]
	e.LastReSeededAt = uint64(time.Now().Add(-48 * time.Hour).UnixMilli())
	store.entries[staleSHA] = e
	store.mu.Unlock()

	pruner := NewRetentionPruner(7 * 24 * time.Hour)
	pruner.SetTracker(tracker)
	pruner.SetManifestStore(store)
	pruner.SetLivenessTTL(24 * time.Hour)
	pruner.Prune(context.Background())

	if !store.IsRevoked(staleSHA) {
		t.Error("expected stale entry to be revoked by liveness sweep")
	}
	if store.IsRevoked(freshSHA) {
		t.Error("fresh entry must not be revoked")
	}
}

// TestRetentionPruner_LivenessTTLZeroDisablesSweep verifies operators
// who explicitly opt out (manifest_liveness_ttl: 0) don't get
// surprised by background revocations.
func TestRetentionPruner_LivenessTTLZeroDisablesSweep(t *testing.T) {
	store := NewManifestStore()
	sha := pruneTestSHA("000abc")
	store.RecordSeed(sha, "x.bin", "*", "7d", "server", 1)
	store.mu.Lock()
	e := store.entries[sha]
	e.LastReSeededAt = uint64(time.Now().Add(-48 * time.Hour).UnixMilli())
	store.entries[sha] = e
	store.mu.Unlock()

	pruner := NewRetentionPruner(7 * 24 * time.Hour)
	pruner.SetManifestStore(store)
	// SetLivenessTTL deliberately omitted (zero default).
	pruner.Prune(context.Background())

	if store.IsRevoked(sha) {
		t.Error("liveness TTL=0 must skip the sweep entirely")
	}
}
