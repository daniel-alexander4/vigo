// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"context"
	"log/slog"
	"time"
)

// RetentionPruner periodically deletes blobs older than the retention window.
type RetentionPruner struct {
	store     *BlobStore
	retention time.Duration
}

// NewRetentionPruner creates a pruner that deletes blobs older than retention.
func NewRetentionPruner(store *BlobStore, retention time.Duration) *RetentionPruner {
	return &RetentionPruner{store: store, retention: retention}
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

// Prune deletes blobs older than the retention window.
func (p *RetentionPruner) Prune(_ context.Context) {
	cutoff := time.Now().Add(-p.retention)

	blobs, err := p.store.List()
	if err != nil {
		slog.Error("swarm retention pruner: list failed", "error", err)
		return
	}

	var pruned int
	for _, b := range blobs {
		if b.CreatedAt.Before(cutoff) {
			if err := p.store.Delete(b.SHA256); err != nil {
				slog.Error("swarm retention pruner: delete failed", "sha256", b.SHA256[:12], "error", err)
				continue
			}
			pruned++
		}
	}

	if pruned > 0 {
		slog.Info("swarm retention pruner complete", "pruned", pruned, "retention", p.retention)
	}
}
