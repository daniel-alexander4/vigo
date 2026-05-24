// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package integrations

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hangingTarget blocks every Dispatch call on a release channel and counts
// concurrent in-flight calls + total Dispatch invocations. Used to drive
// the Dispatcher past dispatcherInflightCap without letting goroutines drain.
type hangingTarget struct {
	name     string
	release  chan struct{}
	inflight int64 // atomic
	peak     int64 // atomic — high-water mark of inflight
	calls    int64 // atomic — total Dispatch invocations that ran
}

func (h *hangingTarget) Name() string             { return h.name }
func (h *hangingTarget) Accepts(_ Event) bool     { return true }
func (h *hangingTarget) Dispatch(ctx context.Context, _ Event) error {
	cur := atomic.AddInt64(&h.inflight, 1)
	defer atomic.AddInt64(&h.inflight, -1)
	atomic.AddInt64(&h.calls, 1)
	// Track peak in-flight via CAS loop.
	for {
		prev := atomic.LoadInt64(&h.peak)
		if cur <= prev || atomic.CompareAndSwapInt64(&h.peak, prev, cur) {
			break
		}
	}
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	return nil
}

// TestDispatcher_InflightCap drives N > dispatcherInflightCap dispatches at a
// hanging target without releasing, and asserts:
//   - The in-flight semaphore (cap(d.inflight)) never holds more than its
//     capacity, so peak concurrent Dispatch invocations on the target is
//     bounded at dispatcherInflightCap.
//   - Overflow attempts drop (Dispatch returns immediately, calls < attempts).
//
// Without the cap, every Dispatch attempt spawns a goroutine that calls
// target.Dispatch — peak in-flight would equal attempts.
func TestDispatcher_InflightCap(t *testing.T) {
	tgt := &hangingTarget{
		name:    "hanging",
		release: make(chan struct{}),
	}
	d := &Dispatcher{
		targets:    []target{tgt},
		suppressor: newEventSuppressor(),
		inflight:   make(chan struct{}, dispatcherInflightCap),
	}

	// Fleet-wide events (empty EnvoyID) bypass the per-envoy suppressor,
	// so every attempt reaches the inflight-cap gate. This isolates the
	// behavior under test from the unrelated suppression-window logic.
	const attempts = dispatcherInflightCap + 64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Dispatch(Event{Name: "test"})
		}()
	}
	wg.Wait() // Dispatch is non-blocking; this returns fast even though target hangs

	// Give the bounded set of accepted dispatches a moment to start their
	// goroutines (they block in target.Dispatch on h.release).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&tgt.inflight) >= int64(dispatcherInflightCap) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	peak := atomic.LoadInt64(&tgt.peak)
	if peak > int64(dispatcherInflightCap) {
		t.Fatalf("peak in-flight %d exceeded cap %d — semaphore not bounding fan-out", peak, dispatcherInflightCap)
	}
	if peak == 0 {
		t.Fatal("peak in-flight 0 — dispatcher never reached the target")
	}

	calls := atomic.LoadInt64(&tgt.calls)
	if calls > int64(dispatcherInflightCap) {
		t.Fatalf("target Dispatch ran %d times — overflow not dropped (expected ≤ %d)", calls, dispatcherInflightCap)
	}
	if calls < int64(dispatcherInflightCap)/2 {
		// Sanity: we should have accepted a substantial fraction; a tiny
		// number would mean the suppressor or some other gate is dropping
		// most of the load before it reaches the inflight check.
		t.Fatalf("target Dispatch ran %d times — far below cap %d, possible upstream gate", calls, dispatcherInflightCap)
	}

	// Drain: release all hanging goroutines so the test exits cleanly.
	close(tgt.release)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&tgt.inflight) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("hanging goroutines did not drain after release")
}
