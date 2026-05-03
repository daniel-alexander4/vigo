// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"testing"
	"time"

	"git.alexander4.io/vigo/server/convergence"
)

// TestUpdateRunStatus_MatchesComputeConvergence pins the property that
// [FleetIndex.UpdateRunStatus] produces the exact same classification as
// [convergence.ComputeConvergence] for every run-status input. Any
// divergence between the two is the root cause of fleet counts and row
// statuses disagreeing in the UI, so this is the load-bearing invariant
// for the envoys page.
func TestUpdateRunStatus_MatchesComputeConvergence(t *testing.T) {
	cases := []struct {
		name         string
		runStatus    string
		changed      int64
		failed       int64
		modules      int64
		wantStatus   string
	}{
		{"clean success", "success", 0, 0, 5, "converged"},
		{"success with changes — single run", "success", 3, 0, 5, "converged"},
		{"partial run with no failures is converged", "partial", 0, 0, 5, "converged"},
		{"partial run with some failures is degraded", "partial", 0, 1, 5, "degraded"},
		{"failure run with full failure ratio", "failure", 0, 5, 5, "failed"},
		{"failure run with high failure ratio", "failure", 0, 4, 5, "failed"},
		// Per-module counts are the truth, not the agent's overall flag.
		// failure with 2/5 (40%) lands in degraded; failure with zero
		// counts is the bookkeeping anomaly we deliberately classify as
		// converged so it doesn't taint the dashboard.
		{"failure run with moderate failure ratio is degraded", "failure", 0, 2, 5, "degraded"},
		{"failure run with no module counts is converged", "failure", 0, 0, 0, "converged"},
		{"blocked run", "blocked", 0, 0, 5, "diverged"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fi := NewFleetIndex()
			fi.AddEnvoy(EnvoyState{
				ID:                "e1",
				Hostname:          "e1.example.com",
				ConvergenceStatus: "no data",
			}, nil)

			fi.UpdateRunStatus("e1", "run-1", tc.runStatus, tc.changed, tc.failed, tc.modules, time.Now())

			got := fi.GetEnvoy("e1")
			if got == nil {
				t.Fatalf("envoy missing after UpdateRunStatus")
			}
			if got.ConvergenceStatus != tc.wantStatus {
				t.Errorf("FleetIndex status = %q, want %q", got.ConvergenceStatus, tc.wantStatus)
			}

			// The row classification must match the canonical algorithm
			// run directly against the same trailing runs.
			canonical := convergence.ComputeConvergence(got.RecentRuns)
			if canonical != got.ConvergenceStatus {
				t.Errorf("ComputeConvergence over RecentRuns = %q, FleetIndex = %q (must match)",
					canonical, got.ConvergenceStatus)
			}
		})
	}
}

// TestUpdateRunStatus_DetectsDrift walks three consecutive changing
// runs and checks that the transition converged → relapsed → diverged
// happens inline, matching the multi-run algorithm rather than the old
// 4-state computeInlineStatus which couldn't see history at all.
func TestUpdateRunStatus_DetectsDrift(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{
		ID:                "e1",
		Hostname:          "e1.example.com",
		ConvergenceStatus: "no data",
	}, nil)

	now := time.Now()

	// Run 1: success with drift → single run is still "converged"
	// (the threshold needs multiple consecutive runs).
	fi.UpdateRunStatus("e1", "r1", "success", 1, 0, 5, now)
	if got := fi.GetEnvoy("e1").ConvergenceStatus; got != "converged" {
		t.Errorf("after run 1: status = %q, want converged", got)
	}

	// Run 2: another success with drift → RelapsedThreshold (2) reached.
	fi.UpdateRunStatus("e1", "r2", "success", 2, 0, 5, now.Add(time.Minute))
	if got := fi.GetEnvoy("e1").ConvergenceStatus; got != "relapsed" {
		t.Errorf("after run 2: status = %q, want relapsed", got)
	}

	// Run 3: third consecutive success with drift → DivergedThreshold (3) reached.
	fi.UpdateRunStatus("e1", "r3", "success", 1, 0, 5, now.Add(2*time.Minute))
	if got := fi.GetEnvoy("e1").ConvergenceStatus; got != "diverged" {
		t.Errorf("after run 3: status = %q, want diverged", got)
	}

	// Run 4: clean run with no changes → back to converged.
	fi.UpdateRunStatus("e1", "r4", "success", 0, 0, 5, now.Add(3*time.Minute))
	if got := fi.GetEnvoy("e1").ConvergenceStatus; got != "converged" {
		t.Errorf("after run 4: status = %q, want converged", got)
	}
}

// TestUpdateRunStatus_RecentRunsBounded pins that the trailing run
// buffer stays bounded at [MaxRecentRuns] entries across many updates.
func TestUpdateRunStatus_RecentRunsBounded(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{
		ID:                "e1",
		Hostname:          "e1.example.com",
		ConvergenceStatus: "no data",
	}, nil)

	for i := 0; i < MaxRecentRuns*3; i++ {
		fi.UpdateRunStatus("e1", "run", "success", 0, 0, 5, time.Now())
	}

	got := fi.GetEnvoy("e1")
	if len(got.RecentRuns) != MaxRecentRuns {
		t.Errorf("RecentRuns length = %d, want %d", len(got.RecentRuns), MaxRecentRuns)
	}
}

// TestConvergenceCounters_DegradedBucket verifies the Degraded bucket
// is incremented by UpdateRunStatus rather than silently bucketing
// into NoData (the old behavior of computeInlineStatus).
func TestConvergenceCounters_DegradedBucket(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{
		ID:                "e1",
		Hostname:          "e1.example.com",
		ConvergenceStatus: "no data",
	}, nil)

	// A partial run with 1 failure out of 5 modules lands squarely
	// in the degraded bucket (1/5 = 20%, below DegradedMaxPct=80).
	fi.UpdateRunStatus("e1", "r1", "partial", 0, 1, 5, time.Now())

	c := fi.ConvergenceSummary()
	if c.Degraded != 1 {
		t.Errorf("Degraded counter = %d, want 1", c.Degraded)
	}
	if c.NoData != 0 {
		t.Errorf("NoData counter = %d, want 0 (must not be misbucketed)", c.NoData)
	}
}
