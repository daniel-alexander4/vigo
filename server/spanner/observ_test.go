// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"testing"
	"time"
)

// Spanner Slice 2-ii-a — observability-cache tests. The cache is NOT a
// CRDT: each peer's entry is replaced wholesale, last-writer-wins by
// the snapshot's own UpdatedAt. These tests exercise update, the
// newer-wins ordering rule, the stale-drop, counter-only updates, and
// the staleness signal.

func TestObservabilityCacheUpdateAndGet(t *testing.T) {
	c := NewObservabilityCache()
	now := time.Now()
	envoys := []EnvoyLite{{ID: "e1", Hostname: "h1"}}
	conv := ConvergenceCounters{Total: 1, Converged: 1}

	if !c.UpdateFull("peerA", envoys, conv, now) {
		t.Fatal("first UpdateFull should be accepted")
	}
	got := c.Get("peerA")
	if got == nil {
		t.Fatal("Get returned nil after a successful update")
	}
	if len(got.Envoys) != 1 || got.Envoys[0].ID != "e1" {
		t.Errorf("envoys not stored: %+v", got.Envoys)
	}
	if got.Convergence.Total != 1 || got.Convergence.Converged != 1 {
		t.Errorf("counters not stored: %+v", got.Convergence)
	}
	if !got.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, now)
	}
}

func TestObservabilityCacheNewerWins(t *testing.T) {
	c := NewObservabilityCache()
	t0 := time.Now()
	t1 := t0.Add(1 * time.Minute)

	c.UpdateFull("peerA", []EnvoyLite{{ID: "old"}}, ConvergenceCounters{Total: 1}, t0)
	if !c.UpdateFull("peerA", []EnvoyLite{{ID: "new"}}, ConvergenceCounters{Total: 2}, t1) {
		t.Fatal("a newer update should be accepted")
	}
	got := c.Get("peerA")
	if got.Envoys[0].ID != "new" || got.Convergence.Total != 2 {
		t.Errorf("newer update did not replace the entry: %+v", got)
	}
}

func TestObservabilityCacheStaleDropped(t *testing.T) {
	c := NewObservabilityCache()
	t0 := time.Now()
	tEarlier := t0.Add(-1 * time.Minute)

	c.UpdateFull("peerA", []EnvoyLite{{ID: "current"}}, ConvergenceCounters{Total: 5}, t0)
	// A stale, out-of-order delivery must be dropped.
	if c.UpdateFull("peerA", []EnvoyLite{{ID: "stale"}}, ConvergenceCounters{Total: 99}, tEarlier) {
		t.Fatal("a stale update should be rejected")
	}
	got := c.Get("peerA")
	if got.Envoys[0].ID != "current" || got.Convergence.Total != 5 {
		t.Errorf("stale update overwrote newer facts: %+v", got)
	}
}

func TestObservabilityCacheUpdateCountersOnly(t *testing.T) {
	c := NewObservabilityCache()
	t0 := time.Now()
	t1 := t0.Add(30 * time.Second)

	// Full snapshot establishes the roster.
	c.UpdateFull("peerA", []EnvoyLite{{ID: "e1"}, {ID: "e2"}}, ConvergenceCounters{Total: 2}, t0)
	// A Tier-1 counter update replaces only the counters; the roster
	// is left intact.
	if !c.UpdateCounters("peerA", ConvergenceCounters{Total: 2, Failed: 1}, t1) {
		t.Fatal("a newer counter update should be accepted")
	}
	got := c.Get("peerA")
	if len(got.Envoys) != 2 {
		t.Errorf("counter update clobbered the roster: %+v", got.Envoys)
	}
	if got.Convergence.Failed != 1 {
		t.Errorf("counters not updated: %+v", got.Convergence)
	}

	// Counter update for an unknown peer creates an entry with no roster.
	if !c.UpdateCounters("peerB", ConvergenceCounters{Total: 7}, t1) {
		t.Fatal("counter update for a new peer should be accepted")
	}
	gotB := c.Get("peerB")
	if gotB == nil || gotB.Convergence.Total != 7 || len(gotB.Envoys) != 0 {
		t.Errorf("new-peer counter entry wrong: %+v", gotB)
	}
}

func TestObservabilityCacheForget(t *testing.T) {
	c := NewObservabilityCache()
	c.UpdateFull("peerA", nil, ConvergenceCounters{}, time.Now())
	c.Forget("peerA")
	if c.Get("peerA") != nil {
		t.Error("Forget did not drop the entry")
	}
}

func TestObservabilityCacheOldestUpdate(t *testing.T) {
	c := NewObservabilityCache()
	if !c.OldestUpdate().IsZero() {
		t.Error("OldestUpdate on an empty cache should be the zero time")
	}
	t0 := time.Now()
	c.UpdateFull("peerA", nil, ConvergenceCounters{}, t0.Add(2*time.Minute))
	c.UpdateFull("peerB", nil, ConvergenceCounters{}, t0)
	c.UpdateFull("peerC", nil, ConvergenceCounters{}, t0.Add(5*time.Minute))
	if !c.OldestUpdate().Equal(t0) {
		t.Errorf("OldestUpdate = %v, want %v (the oldest peer)", c.OldestUpdate(), t0)
	}
}

func TestObservabilityCacheGetReturnsCopy(t *testing.T) {
	c := NewObservabilityCache()
	c.UpdateFull("peerA", []EnvoyLite{{ID: "e1"}}, ConvergenceCounters{}, time.Now())
	got := c.Get("peerA")
	got.Envoys[0].ID = "mutated"
	again := c.Get("peerA")
	if again.Envoys[0].ID != "e1" {
		t.Error("Get returned a mutable reference into cache state")
	}
}

func TestConvergenceCountersAdd(t *testing.T) {
	a := ConvergenceCounters{Total: 3, Converged: 2, Failed: 1, Offline: 1, Changed: 1}
	b := ConvergenceCounters{Total: 4, Converged: 3, Degraded: 1, NoData: 2, Diverged: 1}
	sum := a.Add(b)
	want := ConvergenceCounters{
		Total: 7, Converged: 5, Degraded: 1, Failed: 1, NoData: 2,
		Changed: 1, Diverged: 1, Offline: 1,
	}
	if sum != want {
		t.Errorf("Add = %+v, want %+v", sum, want)
	}
}

// TestObservabilityCacheOwnerOf checks the non-copying envoy→owning-bolt
// lookup that ListRuns(envoy_id) routing depends on.
func TestObservabilityCacheOwnerOf(t *testing.T) {
	c := NewObservabilityCache()
	now := time.Now()
	c.UpdateFull("boltA", []EnvoyLite{{ID: "e1"}, {ID: "e2"}}, ConvergenceCounters{}, now)
	c.UpdateFull("boltB", []EnvoyLite{{ID: "e3"}}, ConvergenceCounters{}, now)

	if owner, ok := c.OwnerOf("e2"); !ok || owner != "boltA" {
		t.Errorf("OwnerOf(e2) = %q, %v; want boltA, true", owner, ok)
	}
	if owner, ok := c.OwnerOf("e3"); !ok || owner != "boltB" {
		t.Errorf("OwnerOf(e3) = %q, %v; want boltB, true", owner, ok)
	}
	if owner, ok := c.OwnerOf("nonexistent"); ok {
		t.Errorf("OwnerOf(nonexistent) = %q, %v; want \"\", false", owner, ok)
	}
}
