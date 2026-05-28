// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"testing"

	"git.alexander4.io/vigo/server/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestCheckInShedding verifies the load-shedding semaphore caps concurrent
// CheckIn handlers and increments the shed counter on overflow.
func TestCheckInShedding(t *testing.T) {
	m := metrics.NewTestMetrics()
	svc := &AgentService{
		metrics:    m,
		checkInSem: make(chan struct{}, 2),
	}

	// Two acquires succeed; counter unchanged.
	for i := 0; i < 2; i++ {
		if !svc.tryAcquireCheckInSlot() {
			t.Fatalf("acquire %d expected to succeed", i+1)
		}
	}
	if got := testutil.ToFloat64(m.CheckinsTotal.WithLabelValues("shed")); got != 0 {
		t.Errorf("shed counter = %v before overflow, want 0", got)
	}

	// Third acquire sheds.
	if svc.tryAcquireCheckInSlot() {
		t.Fatal("third acquire expected to fail (semaphore full)")
	}
	if got := testutil.ToFloat64(m.CheckinsTotal.WithLabelValues("shed")); got != 1 {
		t.Errorf("shed counter = %v after one overflow, want 1", got)
	}

	// Release one slot — next acquire succeeds.
	svc.releaseCheckInSlot()
	if !svc.tryAcquireCheckInSlot() {
		t.Fatal("acquire after release expected to succeed")
	}
	if got := testutil.ToFloat64(m.CheckinsTotal.WithLabelValues("shed")); got != 1 {
		t.Errorf("shed counter = %v after recovery, want 1 (unchanged)", got)
	}
}

// TestCheckInSheddingDisabled verifies that a nil semaphore disables
// shedding entirely — every acquire succeeds, counter never moves.
func TestCheckInSheddingDisabled(t *testing.T) {
	m := metrics.NewTestMetrics()
	svc := &AgentService{
		metrics:    m,
		checkInSem: nil,
	}

	for i := 0; i < 1000; i++ {
		if !svc.tryAcquireCheckInSlot() {
			t.Fatalf("acquire %d expected to succeed when sem is nil", i)
		}
	}
	if got := testutil.ToFloat64(m.CheckinsTotal.WithLabelValues("shed")); got != 0 {
		t.Errorf("shed counter = %v with sem disabled, want 0", got)
	}

	// Release on nil sem must be a safe no-op.
	svc.releaseCheckInSlot()
}
