// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"context"
	"log/slog"
	"time"
)

// RetentionPruner periodically revokes seeds whose retention window has elapsed.
// Revocations propagate fleet-wide via the manifest CRDT, so all envoys evict
// the blob from cache and the original seeder cleans up its seed/ source files.
type RetentionPruner struct {
	tracker       *PeerTracker
	manifestStore *ManifestStore
	retention     time.Duration
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

// Start runs the prune loop at the given interval. Blocks until ctx is cancelled.
func (p *RetentionPruner) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once on startup after a short delay.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		p.Prune(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Prune(ctx)
		}
	}
}

// Prune revokes expired seed entries in the tracker and gossips the
// revocation through the manifest CRDT.
func (p *RetentionPruner) Prune(_ context.Context) {
	var seedsRevoked int
	if p.tracker != nil {
		for _, sha256 := range p.tracker.ExpiredSeeds() {
			p.tracker.RevokeSeed(sha256)
			if p.manifestStore != nil {
				p.manifestStore.Revoke(sha256)
			}
			seedsRevoked++
		}
	}

	if seedsRevoked > 0 {
		slog.Info("swarm retention pruner complete", "seeds_revoked", seedsRevoked)
	}
}
