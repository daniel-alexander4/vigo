// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"testing"
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
