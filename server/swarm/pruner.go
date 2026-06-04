// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"context"
	"log/slog"
	"time"

	"git.alexander4.io/vigo/server/daemon"
)

// RetentionPruner periodically revokes seeds whose retention window has elapsed.
// Revocations propagate fleet-wide via the manifest CRDT, so all envoys evict
// the blob from cache and the original seeder cleans up its seed/ source files.
type RetentionPruner struct {
	tracker       *PeerTracker
	manifestStore *ManifestStore
	retention     time.Duration
	livenessTTL   time.Duration
}

// NewRetentionPruner creates a pruner with the given retention window.
func NewRetentionPruner(retention time.Duration) *RetentionPruner {
	return &RetentionPruner{retention: retention}
}

// SetTracker attaches the peer tracker for seed retention cleanup.
func (p *RetentionPruner) SetTracker(t *PeerTracker) {
	p.tracker = t
}

// SetManifestStore attaches the manifest CRDT so revocations propagate
// to envoys via gossip. Without this, retention-expired blobs would only
// be cleaned up on the original seeder via the check-in sidechannel and
// would remain cached on consumer envoys forever.
func (p *RetentionPruner) SetManifestStore(m *ManifestStore) {
	p.manifestStore = m
}

// SetLivenessTTL configures the liveness sweep added in 0.38.12: any
// active manifest entry whose LastReSeededAt is older than `ttl` is
// revoked on the next prune tick. Zero or negative disables the
// sweep — operators on tiny static fleets can opt out, but the
// default at the call site is 24h.
func (p *RetentionPruner) SetLivenessTTL(ttl time.Duration) {
	p.livenessTTL = ttl
}

// Start runs the prune loop at the given interval. Blocks until ctx is cancelled.
func (p *RetentionPruner) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once on startup after a short delay.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		daemon.Guard("swarm-pruner", func() { p.Prune(ctx) })
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			daemon.Guard("swarm-pruner", func() { p.Prune(ctx) })
		}
	}
}

// Prune revokes expired seed entries in the tracker and gossips the
// revocation through the manifest CRDT, then garbage-collects revoked
// manifest entries older than the retention window. Without the GC
// pass, revoked entries accumulate forever in the in-memory store —
// agents already prune their persisted manifest at the same window
// (manifest.rs::prune_revoked, called from the check-in loop), so this
// keeps the two sides symmetric.
//
// The liveness sweep (added 0.38.12) revokes active entries whose
// LastReSeededAt has aged past `livenessTTL`. The gating in
// `RecordSeedFromAgent` prevents agents from re-creating revoked
// entries via gossip, so once an entry's last seeder goes dark for
// the TTL window it stays revoked until an admin re-distributes.
func (p *RetentionPruner) Prune(_ context.Context) {
	var seedsRevoked, manifestPruned, livenessRevoked int
	if p.tracker != nil {
		for _, sha256 := range p.tracker.ExpiredSeeds() {
			p.tracker.RevokeSeed(sha256)
			if p.manifestStore != nil {
				p.manifestStore.Revoke(sha256)
			}
			seedsRevoked++
		}
	}
	if p.manifestStore != nil && p.livenessTTL > 0 {
		for _, sha256 := range p.manifestStore.StaleActive(p.livenessTTL) {
			if p.tracker != nil {
				p.tracker.RevokeSeed(sha256)
			}
			p.manifestStore.Revoke(sha256)
			livenessRevoked++
		}
	}
	if p.manifestStore != nil {
		manifestPruned = p.manifestStore.PruneRevoked(p.retention)
	}

	if seedsRevoked > 0 || manifestPruned > 0 || livenessRevoked > 0 {
		slog.Info("swarm retention pruner complete",
			"seeds_revoked", seedsRevoked,
			"liveness_revoked", livenessRevoked,
			"manifest_entries_pruned", manifestPruned)
	}
}
