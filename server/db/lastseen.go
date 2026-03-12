// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// LastSeenBatcher accumulates thrall IDs and flushes UpdateLastSeen writes
// periodically, removing per-check-in write latency from the hot path.
type LastSeenBatcher struct {
	mu      sync.Mutex
	pending map[string]struct{}
	queries *Queries
}

// NewLastSeenBatcher creates a batcher backed by the given Queries.
func NewLastSeenBatcher(queries *Queries) *LastSeenBatcher {
	return &LastSeenBatcher{
		pending: make(map[string]struct{}),
		queries: queries,
	}
}

// Mark records a thrall as seen. This is a cheap in-memory operation that
// replaces the synchronous UpdateLastSeen DB write on the check-in path.
func (b *LastSeenBatcher) Mark(id string) {
	b.mu.Lock()
	b.pending[id] = struct{}{}
	b.mu.Unlock()
}

// Start runs the flush loop, writing accumulated last_seen timestamps to the
// DB every interval. Blocks until ctx is cancelled.
func (b *LastSeenBatcher) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final flush on shutdown.
			b.Flush(context.Background())
			return
		case <-ticker.C:
			b.Flush(ctx)
		}
	}
}

// Flush writes all pending last_seen updates to the database and clears
// the pending set. Chunks IDs into batches of 500 to avoid blowing up
// SQLite's query planner with massive IN-clauses.
func (b *LastSeenBatcher) Flush(ctx context.Context) {
	b.mu.Lock()
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	// Swap out the pending map so we don't hold the lock during DB writes.
	batch := b.pending
	b.pending = make(map[string]struct{})
	b.mu.Unlock()

	ids := make([]string, 0, len(batch))
	for id := range batch {
		ids = append(ids, id)
	}

	const chunkSize = 500
	var failed bool
	for i := 0; i < len(ids); i += chunkSize {
		end := i + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		if err := b.queries.BatchUpdateLastSeen(ctx, ids[i:end]); err != nil {
			slog.Error("failed to batch flush last_seen", "count", end-i, "error", err)
			failed = true
		}
	}

	// Re-add all IDs on any failure so they're retried on next flush.
	if failed {
		b.mu.Lock()
		for id := range batch {
			b.pending[id] = struct{}{}
		}
		b.mu.Unlock()
	}
}
