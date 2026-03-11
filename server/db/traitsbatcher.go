package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"
)

// TraitsBatcher accumulates thrall traits and flushes inserts periodically,
// removing per-check-in DB write latency from the hot path. Traits are
// deduplicated by SHA-256 hash — if a thrall's traits haven't changed since
// the last insert, the write is skipped entirely.
type TraitsBatcher struct {
	mu      sync.Mutex
	pending map[string]string    // thrall_id → traits JSON
	hashes  map[string]string    // thrall_id → last written SHA-256
	queries *Queries
}

// NewTraitsBatcher creates a batcher backed by the given Queries.
func NewTraitsBatcher(queries *Queries) *TraitsBatcher {
	return &TraitsBatcher{
		pending: make(map[string]string),
		hashes:  make(map[string]string),
		queries: queries,
	}
}

// Mark records traits for a thrall. Deduplicates by hash — if the traits
// haven't changed since the last flush, the entry is not queued.
func (b *TraitsBatcher) Mark(thrallID, traitsJSON string) {
	h := sha256.Sum256([]byte(traitsJSON))
	hash := hex.EncodeToString(h[:])

	b.mu.Lock()
	if b.hashes[thrallID] == hash {
		b.mu.Unlock()
		return
	}
	b.pending[thrallID] = traitsJSON
	b.hashes[thrallID] = hash
	b.mu.Unlock()
}

// Start runs the flush loop, writing accumulated traits to the DB every
// interval. Blocks until ctx is cancelled.
func (b *TraitsBatcher) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			b.Flush(context.Background())
			return
		case <-ticker.C:
			b.Flush(ctx)
		}
	}
}

// Flush writes all pending traits inserts to the database and clears the
// pending set.
func (b *TraitsBatcher) Flush(ctx context.Context) {
	b.mu.Lock()
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.pending
	b.pending = make(map[string]string)
	b.mu.Unlock()

	for thrallID, traits := range batch {
		if _, err := b.queries.InsertThrallTraits(ctx, InsertThrallTraitsParams{
			ThrallID: thrallID,
			Traits:   traits,
		}); err != nil {
			slog.Error("failed to batch flush traits", "thrall_id", thrallID, "error", err)
			// Don't re-queue — next check-in will re-send traits with a fresh hash comparison.
		}
	}
}
