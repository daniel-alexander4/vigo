// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"git.alexander4.io/vigo/server/testutil"
)

// markJSON adapts the change-key + lazy-build Mark to the tests' intent — dedup
// on the JSON itself and store it (the pre-Piece-2 2-arg behavior).
func markJSON(b *TraitsBatcher, envoyID, traitsJSON string) {
	b.Mark(envoyID, traitsJSON, func() string { return traitsJSON })
}

func TestTraitsBatcher_MarkDeduplicates(t *testing.T) {
	b := NewTraitsBatcher(nil)

	markJSON(b, "envoy-1", `{"os":"linux"}`)
	markJSON(b, "envoy-1", `{"os":"linux"}`) // same hash — should not re-queue

	b.mu.Lock()
	count := len(b.pending)
	b.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 pending entry after dedup, got %d", count)
	}
}

func TestTraitsBatcher_MarkDifferentTraits(t *testing.T) {
	b := NewTraitsBatcher(nil)

	markJSON(b, "envoy-1", `{"os":"linux"}`)
	markJSON(b, "envoy-1", `{"os":"freebsd"}`) // different hash — should replace

	b.mu.Lock()
	count := len(b.pending)
	traits := b.pending["envoy-1"]
	b.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 pending entry, got %d", count)
	}
	if traits != `{"os":"freebsd"}` {
		t.Fatalf("expected updated traits, got %s", traits)
	}
}

func TestTraitsBatcher_MarkMultipleEnvoys(t *testing.T) {
	b := NewTraitsBatcher(nil)

	markJSON(b, "envoy-1", `{"os":"linux"}`)
	markJSON(b, "envoy-2", `{"os":"freebsd"}`)
	markJSON(b, "envoy-1", `{"os":"linux"}`) // dedup

	b.mu.Lock()
	count := len(b.pending)
	b.mu.Unlock()

	if count != 2 {
		t.Fatalf("expected 2 pending entries, got %d", count)
	}
}

func TestTraitsBatcher_FlushClearsPending(t *testing.T) {
	b := NewTraitsBatcher(nil)

	markJSON(b, "envoy-1", `{"os":"linux"}`)

	// Flush with nil queries will panic if it tries to write.
	// Since queries is nil, we just verify the pending map is swapped.
	b.mu.Lock()
	b.pending = make(map[string]string) // simulate flush
	b.mu.Unlock()

	markJSON(b, "envoy-1", `{"os":"linux"}`) // same hash as before — should still be deduped

	b.mu.Lock()
	count := len(b.pending)
	b.mu.Unlock()

	if count != 0 {
		t.Fatalf("expected 0 pending after dedup (hash still cached), got %d", count)
	}
}

func TestTraitsBatcher_FlushResetsForNewTraits(t *testing.T) {
	b := NewTraitsBatcher(nil)

	markJSON(b, "envoy-1", `{"os":"linux"}`)

	// Simulate flush clearing pending but keeping hashes.
	b.mu.Lock()
	b.pending = make(map[string]string)
	b.mu.Unlock()

	// New traits should queue again.
	markJSON(b, "envoy-1", `{"os":"freebsd"}`)

	b.mu.Lock()
	count := len(b.pending)
	b.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 pending after new traits, got %d", count)
	}
}

// TestTraitsBatcher_ConcurrentMark exercises the lock-free dedup path under
// concurrency: many envoys, each Marked from many goroutines with identical
// traits, must yield exactly one pending entry per envoy and race cleanly
// (run with -race). This locks in the contention-free check-in hot path that
// replaced the single global Mark mutex.
func TestTraitsBatcher_ConcurrentMark(t *testing.T) {
	b := NewTraitsBatcher(nil)

	const envoys = 200
	const goroutines = 16

	mark := func() {
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < envoys; i++ {
					// Identical traits per envoy across every goroutine: the
					// first Mark queues, the rest must dedup on the lock-free path.
					markJSON(b, fmt.Sprintf("envoy-%d", i), fmt.Sprintf(`{"id":%d}`, i))
				}
			}()
		}
		wg.Wait()
	}

	mark()
	b.mu.Lock()
	count := len(b.pending)
	b.mu.Unlock()
	if count != envoys {
		t.Fatalf("expected %d pending entries (one per envoy), got %d", envoys, count)
	}

	// Simulate a flush, then a second round of identical traits: every Mark
	// must dedup against the retained hashes — zero new pending work.
	b.mu.Lock()
	b.pending = make(map[string]string)
	b.mu.Unlock()

	mark()
	b.mu.Lock()
	after := len(b.pending)
	b.mu.Unlock()
	if after != 0 {
		t.Fatalf("expected 0 pending after dedup of unchanged traits, got %d", after)
	}
}

// TestTraitsBatcher_MarkSkipsEmptyBlob verifies the ADR-033 Option C guard: when
// build() returns "" (the inventory cache was lost between the change-key check
// and the merge), Mark neither queues an empty snapshot nor advances the key — so
// the next tick with a real blob retries instead of silently persisting nothing.
func TestTraitsBatcher_MarkSkipsEmptyBlob(t *testing.T) {
	b := NewTraitsBatcher(nil)

	b.Mark("envoy-1", "k1", func() string { return "" }) // cache-lost: empty blob

	b.mu.Lock()
	count := len(b.pending)
	b.mu.Unlock()
	if count != 0 {
		t.Fatalf("empty blob must not be queued, got %d pending", count)
	}
	if _, ok := b.keys.Load("envoy-1"); ok {
		t.Error("key must not advance on an empty blob, so the next real tick retries")
	}

	// The retry with the same key now succeeds (proves the key wasn't advanced).
	b.Mark("envoy-1", "k1", func() string { return `{"os":"linux"}` })
	b.mu.Lock()
	got := b.pending["envoy-1"]
	b.mu.Unlock()
	if got != `{"os":"linux"}` {
		t.Fatalf("retry after empty blob should queue the real blob, got %q", got)
	}
}

// TestTraitsBatcher_FlushPrunesToRetentionCap verifies that each flush
// inserts the pending snapshot and prunes the envoy back to the
// 10-snapshot retention cap (DB-04) — the count never sawtooths above
// the cap, regardless of how many flushes accumulate.
func TestTraitsBatcher_FlushPrunesToRetentionCap(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := New(sqlDB)
	ctx := context.Background()

	sqlDB.Exec(`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES ('e1', 'h1', '10.0.0.1', 'key')`)

	b := NewTraitsBatcher(queries)

	// 15 flushes, each with distinct traits so the hash dedup doesn't
	// skip the insert. Each flush adds one row then prunes — the stored
	// count must never exceed the cap.
	for i := 0; i < 15; i++ {
		markJSON(b, "e1", fmt.Sprintf(`{"n":%d}`, i))
		b.Flush(ctx)

		var count int
		if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM envoy_traits WHERE envoy_id = 'e1'`).Scan(&count); err != nil {
			t.Fatalf("count query after flush %d: %v", i+1, err)
		}
		if count > 10 {
			t.Fatalf("after flush %d: envoy_traits count = %d, want <= 10", i+1, count)
		}
	}

	var final int
	sqlDB.QueryRow(`SELECT COUNT(*) FROM envoy_traits WHERE envoy_id = 'e1'`).Scan(&final)
	if final != 10 {
		t.Errorf("final envoy_traits count = %d, want 10", final)
	}
}
