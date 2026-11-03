// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/daemon"
)

// TraitsFlushHook is called after each successful flush with the batch of
// envoy_id → traits JSON that was written. This lets callers update in-memory
// indexes (e.g., FleetIndex) without a circular dependency on the fleet package.
type TraitsFlushHook func(batch map[string]string)

// TraitsBatcher accumulates envoy traits and flushes inserts periodically,
// removing per-check-in DB write latency from the hot path. Traits are
// deduplicated by a caller-supplied change key — if an envoy's traits haven't
// changed since the last insert, the write is skipped entirely. Each flush also
// prunes old snapshots for every envoy it just inserted, enforcing the
// 10-snapshot retention cap (DB-04) coupled to the insert.
type TraitsBatcher struct {
	mu      sync.Mutex        // guards pending + onFlush
	pending map[string]string // envoy_id → traits JSON (changed-since-last-flush set)
	keys    sync.Map          // envoy_id → last-marked change key (string); lock-free dedup on the check-in hot path
	queries *Queries
	onFlush TraitsFlushHook // called after successful DB flush
}

// NewTraitsBatcher creates a batcher backed by the given Queries.
func NewTraitsBatcher(queries *Queries) *TraitsBatcher {
	return &TraitsBatcher{
		pending: make(map[string]string),
		queries: queries,
	}
}

// SetFlushHook registers a callback invoked after each successful flush.
func (b *TraitsBatcher) SetFlushHook(hook TraitsFlushHook) {
	b.mu.Lock()
	b.onFlush = hook
	b.mu.Unlock()
}

// Mark records traits for an envoy, deduplicated by changeKey — a cheap
// fingerprint the caller computes from the parts that actually vary (e.g.
// inventory_hash + a hash of the small vitals), NOT a hash of the full ~155 KB
// merged blob on every tick. The steady-state unchanged case hits a lock-free
// sync.Map load and returns, so the per-check-in traits write does not funnel
// the whole fleet through one mutex. (That mutex was the fleet-scale throughput
// wall: at ~13K envoys @ 1s a single Mark lock serialized ~82% of all check-in
// lock-wait, capping the server below its CPU ceiling.) build() is invoked only
// when the key changed — outside the lock — to materialize the full traits
// string to persist; on a dedup hit it is never called, so the merged blob is
// neither rebuilt nor copied. Writing pending before storing the key keeps the
// invariant "key recorded ⇒ that payload is queued or flushed."
func (b *TraitsBatcher) Mark(envoyID, changeKey string, build func() string) {
	if prev, ok := b.keys.Load(envoyID); ok && prev.(string) == changeKey {
		return
	}

	blob := build()
	if blob == "" {
		// build() couldn't materialize the blob — e.g. the inventory cache was
		// lost (server restart/eviction) between the change-key check and here.
		// Don't persist an empty snapshot or advance the key, so the next tick
		// with a real blob retries. (A genuinely empty traits object serializes
		// as "{}", not "", so this only catches the cache-lost path.)
		return
	}
	b.mu.Lock()
	b.pending[envoyID] = blob
	b.mu.Unlock()
	b.keys.Store(envoyID, changeKey)
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
			daemon.Guard("traits-batcher", func() { b.Flush(ctx) })
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
	hook := b.onFlush
	b.mu.Unlock()

	if err := b.batchInsertTraits(ctx, batch); err != nil {
		slog.Error("failed to batch flush traits", "count", len(batch), "error", err)
		return
	}

	// Prune old snapshots for every envoy that just got a new row,
	// enforcing the 10-snapshot retention cap (DB-04). Coupling the
	// prune to the insert keeps the cap tight without putting DELETE
	// latency on the per-check-in hot path.
	for envoyID := range batch {
		if err := b.queries.PruneEnvoyTraits(ctx, PruneEnvoyTraitsParams{
			EnvoyID:   envoyID,
			EnvoyID_2: envoyID,
		}); err != nil {
			slog.Error("failed to prune envoy traits", "envoy_id", envoyID, "error", err)
		}
	}

	// Update in-memory indexes (e.g., FleetIndex) so traits are visible
	// to UI queries immediately after flush, not just after server restart.
	if hook != nil {
		hook(batch)
	}
}

// batchInsertTraits uses multi-row INSERT to write all pending traits in a
// single round-trip. Chunks at 200 rows (400 params) to stay under SQLite's
// 999 parameter limit.
func (b *TraitsBatcher) batchInsertTraits(ctx context.Context, batch map[string]string) error {
	const chunkSize = 200 // 2 columns × 200 = 400 params
	items := make([]InsertEnvoyTraitsParams, 0, len(batch))
	for envoyID, traits := range batch {
		items = append(items, InsertEnvoyTraitsParams{EnvoyID: envoyID, Traits: traits})
	}

	for i := 0; i < len(items); i += chunkSize {
		end := i + chunkSize
		if end > len(items) {
			end = len(items)
		}
		chunk := items[i:end]

		var sb strings.Builder
		sb.WriteString("INSERT INTO envoy_traits (envoy_id, traits) VALUES ")
		params := make([]interface{}, 0, len(chunk)*2)
		for j, item := range chunk {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?)")
			params = append(params, item.EnvoyID, item.Traits)
		}

		if _, err := b.queries.db.ExecContext(ctx, sb.String(), params...); err != nil {
			return fmt.Errorf("multi-row insert traits (%d rows): %w", len(chunk), err)
		}
	}
	return nil
}
