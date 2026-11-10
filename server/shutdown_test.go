// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/testutil"
)

// Close must not close the DB under a background goroutine that is still
// writing its final batch (the flushers' shutdown Flush).
func TestCloseWaitsForBackgroundBeforeDB(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	s := &Server{db: sqlDB}

	var pingErr atomic.Value
	release := make(chan struct{})
	s.goBG("final-flush", func() {
		<-release
		time.Sleep(100 * time.Millisecond)
		if err := sqlDB.PingContext(context.Background()); err != nil {
			pingErr.Store(err.Error())
		} else {
			pingErr.Store("")
		}
	})
	close(release)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got, _ := pingErr.Load().(string)
	if got != "" || pingErr.Load() == nil {
		t.Fatalf("background goroutine ran against a closed DB (or did not finish before Close): %q", got)
	}
}

// waitBackground is bounded: a goroutine that never exits cannot pin Close.
func TestWaitBackgroundBounded(t *testing.T) {
	s := &Server{}
	stuck := make(chan struct{})
	defer close(stuck)
	s.goBG("stuck", func() { <-stuck })
	start := time.Now()
	s.waitBackground(50 * time.Millisecond)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waitBackground took %v, want ~50ms", d)
	}
}

// fakeStopper models a gRPC server with a held stream: GracefulStop blocks
// until Stop is called.
type fakeStopper struct {
	stopped chan struct{}
	hard    atomic.Bool
}

func (f *fakeStopper) GracefulStop() { <-f.stopped }
func (f *fakeStopper) Stop() {
	if f.hard.CompareAndSwap(false, true) {
		close(f.stopped)
	}
}

// GracefulStop waits on long-lived streams; past the deadline the server is
// stopped hard instead of hanging shutdown.
func TestStopGRPCBoundedForcesStop(t *testing.T) {
	f := &fakeStopper{stopped: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		stopGRPCBounded("test", f, 50*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stopGRPCBounded hung on a server whose GracefulStop never returns")
	}
	if !f.hard.Load() {
		t.Fatal("expected a forced Stop after the graceful deadline")
	}
}
