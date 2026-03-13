// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
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
// interval. Blocks until ctx is cancelled. The offset parameter staggers
// the start relative to other flushers to avoid SQLite write lock contention.
func (b *TraitsBatcher) Start(ctx context.Context, interval, offset time.Duration) {
	if offset > 0 {
		select {
		case <-time.After(offset):
		case <-ctx.Done():
			b.Flush(context.Background())
			return
		}
	}

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

// Flush writes all pending traits inserts to the database using multi-row
// INSERT and clears the pending set.
func (b *TraitsBatcher) Flush(ctx context.Context) {
	b.mu.Lock()
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.pending
	b.pending = make(map[string]string)
	b.mu.Unlock()

	if err := b.batchInsertTraits(ctx, batch); err != nil {
		slog.Error("failed to batch flush traits", "count", len(batch), "error", err)
	}
}

// batchInsertTraits uses multi-row INSERT to write all pending traits in a
// single round-trip. Chunks at 200 rows (400 params) to stay under SQLite's
// 999 parameter limit.
func (b *TraitsBatcher) batchInsertTraits(ctx context.Context, batch map[string]string) error {
	const chunkSize = 200 // 2 columns × 200 = 400 params
	items := make([]InsertThrallTraitsParams, 0, len(batch))
	for thrallID, traits := range batch {
		items = append(items, InsertThrallTraitsParams{ThrallID: thrallID, Traits: traits})
	}

	for i := 0; i < len(items); i += chunkSize {
		end := i + chunkSize
		if end > len(items) {
			end = len(items)
		}
		chunk := items[i:end]

		var sb strings.Builder
		sb.WriteString("INSERT INTO thrall_traits (thrall_id, traits) VALUES ")
		params := make([]interface{}, 0, len(chunk)*2)
		for j, item := range chunk {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?)")
			params = append(params, item.ThrallID, item.Traits)
		}

		if _, err := b.queries.db.ExecContext(ctx, sb.String(), params...); err != nil {
			return fmt.Errorf("multi-row insert traits (%d rows): %w", len(chunk), err)
		}
	}
	return nil
}
