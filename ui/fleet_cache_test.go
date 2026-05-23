// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"testing"
	"time"

	"git.alexander4.io/vigo/server/convergence"
)

// stubFleetSource stands in for the fleet.FleetIndex adapter. It supplies
// both the fleet-wide summary and the per-envoy entry set — FleetCache is
// built entirely from this, so the test exercises the SoT consolidation:
// rows and counts trace to one source, never a parallel DB recompute.
type stubFleetSource struct {
	s       FleetSummary
	entries []FleetStatusEntry
}

func (s *stubFleetSource) ConvergenceSummary() FleetSummary { return s.s }
func (s *stubFleetSource) EnvoyCycle(string) time.Duration  { return 0 }
func (s *stubFleetSource) EnvoyEntries() []FleetStatusEntry { return s.entries }

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

// TestFleetCacheRefreshBuildsFromFleetSource pins that refresh() sources
// its rows from FleetSource.EnvoyEntries (the live FleetIndex) — not a DB
// query and recompute — and enriches each row with the UI-side fields
// (LastSeenLabel, Offline). The per-row Status/Drift are the canonical
// classification carried straight off FleetIndex.
func TestFleetCacheRefreshBuildsFromFleetSource(t *testing.T) {
	fc := NewFleetCache(nil, false, 5*time.Minute, 0)
	fc.SetFleetSource(&stubFleetSource{entries: []FleetStatusEntry{
		{ID: "a", Hostname: "host-a", Status: "converged", LastSeen: time.Now()},
		{ID: "b", Hostname: "host-b", Status: "failed", Drift: convergence.DriftChanged, LastSeen: time.Now()},
	}})

	entries := fc.Entries() // triggers refresh
	if len(entries) != 2 {
		t.Fatalf("Entries: got %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.LastSeenLabel == "" {
			t.Errorf("%s: refresh did not fill LastSeenLabel", e.Hostname)
		}
		if e.Offline {
			t.Errorf("%s: a just-now LastSeen was wrongly marked Offline", e.Hostname)
		}
	}

	// Counts are tallied from the same entry set the rows come from, so
	// the pills and the table can never disagree.
	fc.mu.RLock()
	c := fc.counts
	fc.mu.RUnlock()
	if c.Total != 2 || c.Converged != 1 || c.Failed != 1 || c.Changed != 1 {
		t.Errorf("counts not tallied from entries: %+v", c)
	}
}
