// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestSafeGo_RecoversPanic proves a panic inside the goroutine is recovered —
// if it weren't, the panic would unwind past the test goroutine and crash the
// whole test binary. The inner `defer close(done)` runs as the panic unwinds
// through fn, before SafeGo's recover catches it, so the test can observe that
// fn actually ran and then survive the panic.
func TestSafeGo_RecoversPanic(t *testing.T) {
	done := make(chan struct{})
	SafeGo("test-panic", func() {
		defer close(done)
		panic("boom")
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fn did not run")
	}
	// If the panic had escaped, the binary would already be gone. Give the
	// deferred recover a beat to complete before the test returns.
	time.Sleep(20 * time.Millisecond)
}

// TestSafeGo_RunsNormalFn confirms a non-panicking fn runs to completion.
func TestSafeGo_RunsNormalFn(t *testing.T) {
	done := make(chan struct{})
	SafeGo("test-normal", func() { close(done) })

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fn did not run")
	}
}

// TestSafeGo_DoesNotRestart confirms SafeGo runs fn exactly once — it recovers
// and lets the goroutine die, it does not re-invoke fn (setup-phase panics are
// deterministic; self-healing of repeating work is Guard's job, not SafeGo's).
// A panicking fn that incremented on each entry would climb past 1 if SafeGo
// restarted it.
func TestSafeGo_DoesNotRestart(t *testing.T) {
	var calls int32
	ran := make(chan struct{})
	SafeGo("test-once", func() {
		atomic.AddInt32(&calls, 1)
		close(ran)
		panic("boom")
	})

	<-ran
	// Wait well past any plausible restart window.
	time.Sleep(100 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("expected fn to run exactly once, ran %d times", n)
	}
}

// TestGuard_RecoversAndReturns proves Guard recovers a panicking fn and returns
// normally, so the calling loop can continue. If the panic escaped, the
// statement after Guard would never execute (the goroutine would unwind).
func TestGuard_RecoversAndReturns(t *testing.T) {
	reached := false
	Guard("test-tick", func() { panic("boom") })
	reached = true // only runs if Guard swallowed the panic
	if !reached {
		t.Fatal("Guard did not recover the panic")
	}
}

// TestGuard_LoopSurvivesTransientPanic models the real use: a loop whose tick
// work panics on some iterations. Guard must let the loop run every iteration
// and self-heal the panicking ones — proving a transient fault doesn't kill the
// daemon.
func TestGuard_LoopSurvivesTransientPanic(t *testing.T) {
	const iters = 5
	completed := 0
	for i := 0; i < iters; i++ {
		i := i
		Guard("test-loop", func() {
			if i%2 == 0 {
				panic("transient")
			}
			completed++
		})
	}
	// Iterations 1 and 3 complete; 0, 2, 4 panic and are recovered. The loop
	// ran all 5 regardless.
	if completed != 2 {
		t.Fatalf("expected 2 healthy ticks to complete, got %d", completed)
	}
}

// TestGuard_RunsNormalFn confirms a non-panicking fn runs to completion.
func TestGuard_RunsNormalFn(t *testing.T) {
	ran := false
	Guard("test-normal", func() { ran = true })
	if !ran {
		t.Fatal("Guard did not run fn")
	}
}

// TestRecover_NilOnPanic proves Recover with a nil teardown recovers a panic so
// the deferring function returns normally. If the panic escaped, the statement
// after the panicking call would never run.
func TestRecover_NilOnPanic(t *testing.T) {
	reached := false
	func() {
		defer Recover("test-recover", nil)
		panic("boom")
	}()
	reached = true
	if !reached {
		t.Fatal("Recover did not contain the panic")
	}
}

// TestRecover_RunsOnPanicTeardown proves the onPanic teardown runs after a
// recovered panic — and does NOT run when fn returns normally.
func TestRecover_RunsOnPanicTeardown(t *testing.T) {
	var got any
	func() {
		defer Recover("test-teardown", func(r any) { got = r })
		panic("boom")
	}()
	if got != "boom" {
		t.Fatalf("onPanic teardown did not receive the panic value: got %v", got)
	}

	tornDown := false
	func() {
		defer Recover("test-teardown", func(any) { tornDown = true })
		// no panic
	}()
	if tornDown {
		t.Fatal("onPanic teardown ran without a panic")
	}
}

// TestRecover_MutatesNamedReturn proves the onPanic teardown can set the
// enclosing function's named return value — the mechanism the gRPC recovery
// interceptor relies on to turn a handler panic into a codes.Internal error.
func TestRecover_MutatesNamedReturn(t *testing.T) {
	got := callWithNamedReturn(true)
	if got != "recovered" {
		t.Fatalf("expected named return set to %q on panic, got %q", "recovered", got)
	}
	if got := callWithNamedReturn(false); got != "ok" {
		t.Fatalf("expected named return %q on clean run, got %q", "ok", got)
	}
}

// callWithNamedReturn returns "ok" normally, or "recovered" if it panics — the
// latter set by the onPanic teardown via the named return.
func callWithNamedReturn(boom bool) (result string) {
	defer Recover("test-named-return", func(any) { result = "recovered" })
	if boom {
		panic("boom")
	}
	return "ok"
}
