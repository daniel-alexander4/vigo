// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"encoding/base64"
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

// panicTarget panics on every Dispatch call. Used to verify the per-target
// goroutine's recover defer catches panics without taking the dispatcher
// down or starving sibling targets.
type panicTarget struct {
	name  string
	calls int64 // atomic — total Dispatch invocations that ran
}

func (p *panicTarget) Name() string         { return p.name }
func (p *panicTarget) Accepts(_ Event) bool { return true }
func (p *panicTarget) Dispatch(_ context.Context, _ Event) error {
	atomic.AddInt64(&p.calls, 1)
	panic("synthetic target panic")
}

// TestDispatcher_RecoversFromTargetPanic registers a panicking target alongside
// a healthy hanging target, fires N dispatches, and asserts:
//   - Dispatch() never propagates the panic (test reaches the end).
//   - The panicking target's Dispatch ran N times (recover catches each one;
//     the loop in Dispatch isn't broken by an earlier panic).
//   - The healthy target also ran N times (the panic in a sibling goroutine
//     didn't starve other targets in the same fan-out).
//   - The inflight semaphore drains to zero (no slot leak — the inflight
//     release defer runs even on panic, and stays consistent with recover).
func TestDispatcher_RecoversFromTargetPanic(t *testing.T) {
	pt := &panicTarget{name: "panicker"}
	ht := &hangingTarget{name: "healthy", release: make(chan struct{})}
	d := &Dispatcher{
		targets:    []target{pt, ht},
		suppressor: newEventSuppressor(),
		inflight:   make(chan struct{}, dispatcherInflightCap),
	}

	const attempts = 8
	for i := 0; i < attempts; i++ {
		d.Dispatch(Event{Name: "test"})
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&pt.calls) >= int64(attempts) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&pt.calls); got < int64(attempts) {
		t.Fatalf("panicking target ran %d times, expected %d (recover may not be reached)", got, attempts)
	}

	// Release the healthy target so its goroutines complete; assert the
	// per-event fan-out reached it for every event. A panic in pt's
	// goroutine must not prevent ht's sibling goroutine from running.
	close(ht.release)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&ht.inflight) == 0 && atomic.LoadInt64(&ht.calls) >= int64(attempts) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&ht.calls); got < int64(attempts) {
		t.Fatalf("healthy target ran %d times, expected %d — fan-out broken by sibling panic", got, attempts)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(d.inflight) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("inflight slot count %d after drain — panic leaked a slot", len(d.inflight))
}

// basicAuth builds the Authorization header for eight outbound
// integrations (ConnectWise, ServiceNow, Jira, Zendesk, …). It must
// match RFC 4648 standard base64 with padding exactly — a divergence
// would silently break every one of those Basic-auth handshakes.
func TestBasicAuth(t *testing.T) {
	cases := []string{
		"",                    // empty
		"a",                   // 1-byte tail → "==" padding
		"ab",                  // 2-byte tail → "=" padding
		"abc",                 // exact 3-byte group, no padding
		"user:pass",           // typical credential
		"company+public:priv", // ConnectWise-shaped
		"api-token:X",         // Freshservice-shaped
	}
	for _, in := range cases {
		want := base64.StdEncoding.EncodeToString([]byte(in))
		if got := basicAuth(in); got != want {
			t.Errorf("basicAuth(%q) = %q, want %q", in, got, want)
		}
	}
}
