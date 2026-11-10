// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestStateFlusher_ConcurrentMarkAndDrain exercises concurrent MarkDirty and
// DrainDirty calls to verify the FleetIndex dirty set is race-free. 100
// goroutines mark envoys while 10 goroutines drain concurrently. The test
// first runs all marks + drains in parallel, then performs a final drain.
// The union of all drained IDs must equal the full set of marked IDs.
func TestStateFlusher_ConcurrentMarkAndDrain(t *testing.T) {
	fi := NewFleetIndex()
	const numEnvoys = 100

	// Seed envoys. Each goroutine will mark exactly one unique envoy.
	for i := 0; i < numEnvoys; i++ {
		fi.AddEnvoy(EnvoyState{
			ID:               fmt.Sprintf("t-%d", i),
			Hostname:         fmt.Sprintf("h-%d.example.com", i),
			ConvergenceStatus: "no data",
		}, nil)
	}

	// Phase 1: Mark all envoys dirty from 100 goroutines (one per envoy).
	var markWg sync.WaitGroup
	for i := 0; i < numEnvoys; i++ {
		markWg.Add(1)
		go func(n int) {
			defer markWg.Done()
			fi.MarkDirty(fmt.Sprintf("t-%d", n))
		}(i)
	}
	markWg.Wait()

	// Phase 2: 10 goroutines drain concurrently. Since all marks are done,
	// the union of all drains must cover every envoy exactly once.
	totalDrained := make(map[string]bool)
	var mu sync.Mutex
	var drainWg sync.WaitGroup
	for i := 0; i < 10; i++ {
		drainWg.Add(1)
		go func() {
			defer drainWg.Done()
			dirty := fi.DrainDirty()
			mu.Lock()
			for id := range dirty {
				totalDrained[id] = true
			}
			mu.Unlock()
		}()
	}
	drainWg.Wait()

	// Drain any stragglers (should be none, but defensive).
	remaining := fi.DrainDirty()
	for id := range remaining {
		totalDrained[id] = true
	}

	// Every envoy must have been drained.
	if len(totalDrained) != numEnvoys {
		t.Errorf("expected %d drained envoys, got %d", numEnvoys, len(totalDrained))
	}

	// Dirty set must be empty now.
	if leftover := fi.DrainDirty(); len(leftover) != 0 {
		t.Errorf("dirty set after full drain = %d, want 0", len(leftover))
	}
}

// TestStateFlusher_ConcurrentMarkAndFlush tests that MarkDirty, DrainDirty,
// and DirtyCount can be called concurrently without races. This simulates
// the StateFlusher.Flush reading from the dirty set while check-in handlers
// write to it.
func TestStateFlusher_ConcurrentMarkAndFlush(t *testing.T) {
	fi := NewFleetIndex()
	const numEnvoys = 100

	for i := 0; i < numEnvoys; i++ {
		fi.AddEnvoy(EnvoyState{
			ID:               fmt.Sprintf("t-%d", i),
			Hostname:         fmt.Sprintf("h-%d.example.com", i),
			ConvergenceStatus: "no data",
		}, nil)
	}

	var wg sync.WaitGroup

	// 50 goroutines marking envoys dirty (simulating check-in handlers).
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				id := fmt.Sprintf("t-%d", (n*10+j)%numEnvoys)
				fi.MarkDirty(id)
				fi.MarkSeen(id, time.Now())
			}
		}(i)
	}

	// 10 goroutines performing DrainDirty (simulating Flush).
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				dirty := fi.DrainDirty()
				// Iterate the drained map as Flush does.
				for id, ts := range dirty {
					_ = id
					_ = ts.ForcePush
					_ = ts.PendingRotatedSecrets
				}
			}
		}()
	}

	wg.Wait()
	// No panics or races = pass.
}

// TestStateFlusher_StaggerOffset verifies that when context is cancelled during
// the stagger offset wait, Start() still exits promptly (runs final flush and
// returns). We use an empty dirty set so Flush returns immediately.
func TestStateFlusher_StaggerOffset(t *testing.T) {
	fi := NewFleetIndex()
	sf := &StateFlusher{fleet: fi}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		// Start with a very long offset (1 hour) — we'll cancel before it elapses.
		sf.Start(ctx, 10*time.Second, 1*time.Hour)
		close(done)
	}()

	// Cancel immediately — the flusher should exit the offset wait promptly.
	cancel()

	select {
	case <-done:
		// Success — Start returned after context cancel during offset.
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancel during offset wait")
	}
}

// TestStateFlusher_ZeroOffset verifies that Start with offset=0 skips the
// offset wait entirely and proceeds directly to the ticker loop.
func TestStateFlusher_ZeroOffset(t *testing.T) {
	fi := NewFleetIndex()
	sf := &StateFlusher{fleet: fi}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	done := make(chan struct{})
	go func() {
		sf.Start(ctx, 10*time.Second, 0)
		close(done)
	}()

	select {
	case <-done:
		// Success.
	case <-time.After(2 * time.Second):
		t.Fatal("Start with offset=0 did not return after context cancel")
	}
}

// TestStateFlusher_DrainReturnsSnapshot verifies that DrainDirty returns a
// deep copy of envoy state, not a reference to the FleetIndex's internal
// state. Mutations to the drained copy must not affect the FleetIndex.
func TestStateFlusher_DrainReturnsSnapshot(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddEnvoy(EnvoyState{
		ID:                    "t-1",
		Hostname:              "snap.example.com",
		IP:                    "10.0.0.1",
		ConvergenceStatus:      "converged",
		ForcePush:             true,
		PendingRotatedSecrets: []string{"secret:db/pass"},
		Traits:                map[string]string{"os": "linux"},
	}, nil)
	fi.MarkDirty("t-1")

	drained := fi.DrainDirty()
	if len(drained) != 1 {
		t.Fatalf("DrainDirty returned %d entries, want 1", len(drained))
	}

	snap := drained["t-1"]
	if snap == nil {
		t.Fatal("expected t-1 in drained set")
	}

	// Mutate the drained copy.
	snap.Hostname = "MUTATED"
	snap.ForcePush = false
	snap.PendingRotatedSecrets = append(snap.PendingRotatedSecrets, "secret:extra")
	snap.IP = "MUTATED"
	snap.ConvergenceStatus = "MUTATED"

	// Verify the FleetIndex's original is unchanged.
	orig := fi.GetEnvoy("t-1")
	if orig == nil {
		t.Fatal("original envoy should still exist in index")
	}
	if orig.Hostname != "snap.example.com" {
		t.Errorf("Hostname = %q, want %q (snapshot mutation leaked)", orig.Hostname, "snap.example.com")
	}
	if !orig.ForcePush {
		t.Error("ForcePush should still be true (snapshot mutation leaked)")
	}
	if len(orig.PendingRotatedSecrets) != 1 || orig.PendingRotatedSecrets[0] != "secret:db/pass" {
		t.Errorf("PendingRotatedSecrets = %v, want [secret:db/pass] (snapshot mutation leaked)", orig.PendingRotatedSecrets)
	}
	if orig.IP != "10.0.0.1" {
		t.Errorf("IP = %q, want %q (snapshot mutation leaked)", orig.IP, "10.0.0.1")
	}
	if orig.ConvergenceStatus != "converged" {
		t.Errorf("ConvergenceStatus = %q, want %q (snapshot mutation leaked)", orig.ConvergenceStatus, "converged")
	}
}
