// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// TraitsPruner accumulates thrall IDs that had traits inserted and
// periodically prunes old trait snapshots, removing per-check-in DELETE
// latency from the hot path.
type TraitsPruner struct {
	mu      sync.Mutex
	pending map[string]struct{}
	queries *Queries
}

// NewTraitsPruner creates a pruner backed by the given Queries.
func NewTraitsPruner(queries *Queries) *TraitsPruner {
	return &TraitsPruner{
		pending: make(map[string]struct{}),
		queries: queries,
	}
}

// Mark records a thrall as needing trait pruning.
func (p *TraitsPruner) Mark(id string) {
	p.mu.Lock()
	p.pending[id] = struct{}{}
	p.mu.Unlock()
}

// Start runs the prune loop at the given interval. Blocks until ctx is cancelled.
func (p *TraitsPruner) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.Flush(context.Background())
			return
		case <-ticker.C:
			p.Flush(ctx)
		}
	}
}

// Flush prunes trait snapshots for all pending thralls and clears the set.
func (p *TraitsPruner) Flush(ctx context.Context) {
	p.mu.Lock()
	if len(p.pending) == 0 {
		p.mu.Unlock()
		return
	}
	batch := p.pending
	p.pending = make(map[string]struct{})
	p.mu.Unlock()

	for id := range batch {
		if err := p.queries.PruneThrallTraits(ctx, PruneThrallTraitsParams{
			ThrallID:   id,
			ThrallID_2: id,
		}); err != nil {
			slog.Error("failed to prune thrall traits", "thrall_id", id, "error", err)
		}
	}
}
