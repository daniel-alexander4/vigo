// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"context"
	"log/slog"
	"time"
)

// RetentionPruner periodically retires expired seed entries from the PeerTracker.
type RetentionPruner struct {
	tracker   *PeerTracker
	retention time.Duration
}

// NewRetentionPruner creates a pruner with the given retention window.
func NewRetentionPruner(retention time.Duration) *RetentionPruner {
	return &RetentionPruner{retention: retention}
}

// SetTracker attaches the peer tracker for seed retention cleanup.
func (p *RetentionPruner) SetTracker(t *PeerTracker) {
	p.tracker = t
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

// Prune retires expired seed entries in the tracker.
func (p *RetentionPruner) Prune(_ context.Context) {
	var seedsRetired int
	if p.tracker != nil {
		for _, sha256 := range p.tracker.ExpiredSeeds() {
			p.tracker.RetireSeed(sha256)
			seedsRetired++
		}
	}

	if seedsRetired > 0 {
		slog.Info("swarm retention pruner complete", "seeds_retired", seedsRetired)
	}
}
