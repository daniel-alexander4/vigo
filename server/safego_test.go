// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestSafeGo_RecoversPanic proves a panic inside the goroutine is recovered —
// if it weren't, the panic would unwind past the test goroutine and crash the
// whole test binary. The inner `defer close(done)` runs as the panic unwinds
// through fn, before safeGo's recover catches it, so the test can observe that
// fn actually ran and then survive the panic.
func TestSafeGo_RecoversPanic(t *testing.T) {
	done := make(chan struct{})
	safeGo("test-panic", func() {
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
	safeGo("test-normal", func() { close(done) })

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fn did not run")
	}
}

// TestSafeGo_DoesNotRestart confirms safeGo runs fn exactly once — it recovers
// and lets the goroutine die, it does not re-invoke fn (deliberate: several of
// the real daemons are not re-entrant). A panicking fn that incremented on each
// entry would climb past 1 if safeGo restarted it.
func TestSafeGo_DoesNotRestart(t *testing.T) {
	var calls int32
	ran := make(chan struct{})
	safeGo("test-once", func() {
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
