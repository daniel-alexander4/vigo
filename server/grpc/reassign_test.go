// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"sync"
	"testing"
	"time"
)

func TestReassignStore_PopOnEmptyStore(t *testing.T) {
	s := NewReassignStore()
	info, ok := s.Pop("envoy-1")
	if ok || info != (ReassignInfo{}) {
		t.Errorf("Pop on empty store = (%v, %v), want zero/false", info, ok)
	}
}

func TestReassignStore_PopMissingKey(t *testing.T) {
	s := NewReassignStore()
	s.Set("envoy-other", ReassignInfo{TargetAddr: "x", NewEnvoyID: "y"})

	info, ok := s.Pop("envoy-1")
	if ok || info != (ReassignInfo{}) {
		t.Errorf("Pop on missing key = (%v, %v), want zero/false", info, ok)
	}

	// The other entry must still be present.
	info, ok = s.Pop("envoy-other")
	if !ok || info.TargetAddr != "x" || info.NewEnvoyID != "y" {
		t.Errorf("subsequent Pop = (%v, %v), want present", info, ok)
	}
}

func TestReassignStore_PopHit(t *testing.T) {
	s := NewReassignStore()
	want := ReassignInfo{TargetAddr: "bolt-2:1530", NewEnvoyID: "new-id"}
	s.Set("envoy-1", want)

	got, ok := s.Pop("envoy-1")
	if !ok || got != want {
		t.Errorf("Pop hit = (%v, %v), want (%v, true)", got, ok, want)
	}

	// Second Pop is a miss — entry was deleted.
	got, ok = s.Pop("envoy-1")
	if ok || got != (ReassignInfo{}) {
		t.Errorf("Pop after delete = (%v, %v), want zero/false", got, ok)
	}
}

// TestReassignStore_PopEmptyFastPathDoesNotBlockOnWriter verifies the
// empty-store fast path returns under RLock alone — it must not block on
// a held write lock. Holding s.mu.Lock would freeze every concurrent
// CheckIn at fleet scale if the slow path were on the empty case.
func TestReassignStore_PopEmptyFastPathDoesNotBlockOnWriter(t *testing.T) {
	s := NewReassignStore()

	// Take the write lock and hold it; a goroutine doing Pop on the empty
	// store must NOT block on this.
	s.mu.Lock()

	done := make(chan struct{})
	go func() {
		_, _ = s.Pop("any-envoy")
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Pop on empty store should be blocked by held write lock " +
			"but completed — fast path may be incorrect")
	case <-time.After(50 * time.Millisecond):
		// Expected: Pop is blocked on RLock because Lock is held.
	}

	// Release the writer; the Pop goroutine must now finish.
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Pop did not complete after writer released the lock")
	}
}

// TestReassignStore_PopConcurrent exercises the race-prone hit-path
// re-check: many goroutines all try to Pop the same key while a writer
// keeps re-setting it. With -race, this catches any unsynchronised
// map access in the double-checked locking.
func TestReassignStore_PopConcurrent(t *testing.T) {
	s := NewReassignStore()

	const iterations = 1000
	const popWorkers = 16

	var setterDone sync.WaitGroup
	setterDone.Add(1)
	stop := make(chan struct{})
	go func() {
		defer setterDone.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			s.Set("envoy-1", ReassignInfo{TargetAddr: "x"})
			if i%100 == 0 {
				s.Set("envoy-2", ReassignInfo{TargetAddr: "y"})
			}
		}
	}()

	var popperDone sync.WaitGroup
	var hits int64
	var hitsMu sync.Mutex
	for w := 0; w < popWorkers; w++ {
		popperDone.Add(1)
		go func() {
			defer popperDone.Done()
			for i := 0; i < iterations; i++ {
				if _, ok := s.Pop("envoy-1"); ok {
					hitsMu.Lock()
					hits++
					hitsMu.Unlock()
				}
				_, _ = s.Pop("envoy-missing")
			}
		}()
	}

	popperDone.Wait()
	close(stop)
	setterDone.Wait()

	if hits == 0 {
		t.Errorf("expected at least some Pop hits across %d workers × %d iterations, got 0",
			popWorkers, iterations)
	}
}
