// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"testing"
	"time"
)

// stubFleetSource returns a fixed FleetSummary for FleetCache.Counts to
// consume — used to verify the SoT consolidation: when wired, the
// dashboard pills come from the live convergence counters (this stub
// stands in for fleet.FleetIndex), not from the per-entry rebuild.
type stubFleetSource struct {
	s FleetSummary
}

func (s *stubFleetSource) ConvergenceSummary() FleetSummary { return s.s }

func TestFleetCacheCountsPrefersFleetSource(t *testing.T) {
	fc := &FleetCache{updatedAt: time.Now()}
	// Simulate a refresh having seeded FleetCache's internal
	// FleetStatusCounts with a stale value plus an Observing count
	// that the FleetSource cannot supply.
	fc.counts = FleetStatusCounts{
		Total:     5,
		Converged: 5,
		Observing: 2,
	}

	// Wire a FleetSource with different live values. Counts() must
	// return the FleetSource numbers for every failure / drift /
	// reachability bucket, and keep Observing from the cached entries.
	fc.SetFleetSource(&stubFleetSource{s: FleetSummary{
		Total:     10,
		Converged: 8,
		Degraded:  1,
		Failed:    1,
		NoData:    0,
		Changed:   3,
		Diverged:  1,
		Offline:   2,
	}})

	got := fc.Counts()

	if got.Total != 10 {
		t.Errorf("Total: got %d, want 10 (live)", got.Total)
	}
	if got.Converged != 8 {
		t.Errorf("Converged: got %d, want 8 (live)", got.Converged)
	}
	if got.Failed != 1 {
		t.Errorf("Failed: got %d, want 1 (live)", got.Failed)
	}
	if got.Changed != 3 {
		t.Errorf("Changed: got %d, want 3 (live)", got.Changed)
	}
	if got.Diverged != 1 {
		t.Errorf("Diverged: got %d, want 1 (live)", got.Diverged)
	}
	if got.Observing != 2 {
		t.Errorf("Observing: got %d, want 2 (cached)", got.Observing)
	}
}

func TestFleetCacheCountsFallsBackWhenNoFleetSource(t *testing.T) {
	fc := &FleetCache{updatedAt: time.Now()}
	fc.counts = FleetStatusCounts{
		Total:     3,
		Converged: 3,
		Observing: 1,
	}

	got := fc.Counts()
	if got.Total != 3 || got.Converged != 3 || got.Observing != 1 {
		t.Errorf("fallback path returned wrong counts: %+v", got)
	}
}
