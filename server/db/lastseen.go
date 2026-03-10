package db

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// LastSeenBatcher accumulates node IDs and flushes UpdateLastSeen writes
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

// Mark records a node as seen. This is a cheap in-memory operation that
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
// the pending set. Uses a single UPDATE ... WHERE id IN (...) query to
// collapse N round-trips into one.
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
	if err := b.queries.BatchUpdateLastSeen(ctx, ids); err != nil {
		slog.Error("failed to batch flush last_seen", "count", len(ids), "error", err)
		// Re-add failed IDs so they're retried on next flush.
		b.mu.Lock()
		for id := range batch {
			b.pending[id] = struct{}{}
		}
		b.mu.Unlock()
	}
}
