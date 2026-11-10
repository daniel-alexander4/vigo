// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"testing"

	"git.alexander4.io/vigo/server/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestReportResultShedding verifies the admission semaphore caps concurrent
// ReportResult handlers and increments the shed counter on overflow.
func TestReportResultShedding(t *testing.T) {
	m := metrics.NewTestMetrics()
	svc := &AgentService{
		metrics:         m,
		reportResultSem: make(chan struct{}, 2),
	}

	for i := 0; i < 2; i++ {
		if !svc.tryAcquireReportSlot() {
			t.Fatalf("acquire %d expected to succeed", i+1)
		}
	}
	if got := testutil.ToFloat64(m.ReportsTotal.WithLabelValues("shed")); got != 0 {
		t.Errorf("shed counter = %v before overflow, want 0", got)
	}

	if svc.tryAcquireReportSlot() {
		t.Fatal("third acquire expected to fail (semaphore full)")
	}
	if got := testutil.ToFloat64(m.ReportsTotal.WithLabelValues("shed")); got != 1 {
		t.Errorf("shed counter = %v after one overflow, want 1", got)
	}

	svc.releaseReportSlot()
	if !svc.tryAcquireReportSlot() {
		t.Fatal("acquire after release expected to succeed")
	}
	if got := testutil.ToFloat64(m.ReportsTotal.WithLabelValues("shed")); got != 1 {
		t.Errorf("shed counter = %v after recovery, want 1 (unchanged)", got)
	}
}

// TestReportResultSheddingDisabled verifies a nil semaphore disables shedding —
// every acquire succeeds and the counter never moves.
func TestReportResultSheddingDisabled(t *testing.T) {
	m := metrics.NewTestMetrics()
	svc := &AgentService{metrics: m, reportResultSem: nil}

	for i := 0; i < 1000; i++ {
		if !svc.tryAcquireReportSlot() {
			t.Fatalf("acquire %d expected to succeed when sem is nil", i)
		}
	}
	if got := testutil.ToFloat64(m.ReportsTotal.WithLabelValues("shed")); got != 0 {
		t.Errorf("shed counter = %v with sem disabled, want 0", got)
	}
	svc.releaseReportSlot() // safe no-op on nil sem
}
