// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"fmt"
	"testing"

	"git.alexander4.io/vigo/server/testutil"
)

func TestTraitsBatcher_MarkDeduplicates(t *testing.T) {
	b := NewTraitsBatcher(nil)

	b.Mark("envoy-1", `{"os":"linux"}`)
	b.Mark("envoy-1", `{"os":"linux"}`) // same hash — should not re-queue

	b.mu.Lock()
	count := len(b.pending)
	b.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 pending entry after dedup, got %d", count)
	}
}

func TestTraitsBatcher_MarkDifferentTraits(t *testing.T) {
	b := NewTraitsBatcher(nil)

	b.Mark("envoy-1", `{"os":"linux"}`)
	b.Mark("envoy-1", `{"os":"freebsd"}`) // different hash — should replace

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

	b.Mark("envoy-1", `{"os":"linux"}`)
	b.Mark("envoy-2", `{"os":"freebsd"}`)
	b.Mark("envoy-1", `{"os":"linux"}`) // dedup

	b.mu.Lock()
	count := len(b.pending)
	b.mu.Unlock()

	if count != 2 {
		t.Fatalf("expected 2 pending entries, got %d", count)
	}
}

func TestTraitsBatcher_FlushClearsPending(t *testing.T) {
	b := NewTraitsBatcher(nil)

	b.Mark("envoy-1", `{"os":"linux"}`)

	// Flush with nil queries will panic if it tries to write.
	// Since queries is nil, we just verify the pending map is swapped.
	b.mu.Lock()
	b.pending = make(map[string]string) // simulate flush
	b.mu.Unlock()

	b.Mark("envoy-1", `{"os":"linux"}`) // same hash as before — should still be deduped

	b.mu.Lock()
	count := len(b.pending)
	b.mu.Unlock()

	if count != 0 {
		t.Fatalf("expected 0 pending after dedup (hash still cached), got %d", count)
	}
}

func TestTraitsBatcher_FlushResetsForNewTraits(t *testing.T) {
	b := NewTraitsBatcher(nil)

	b.Mark("envoy-1", `{"os":"linux"}`)

	// Simulate flush clearing pending but keeping hashes.
	b.mu.Lock()
	b.pending = make(map[string]string)
	b.mu.Unlock()

	// New traits should queue again.
	b.Mark("envoy-1", `{"os":"freebsd"}`)

	b.mu.Lock()
	count := len(b.pending)
	b.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 pending after new traits, got %d", count)
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
		b.Mark("e1", fmt.Sprintf(`{"n":%d}`, i))
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
