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
		{"blocked run is failed (no resources applied)", "blocked", 0, 0, 5, "failed"},
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

// TestUpdateRunStatus_DetectsDrift walks several consecutive changing
// runs and checks that the drift-axis classification transitions
// None → Changed → Diverged inline. Failure-axis status stays
// Converged throughout (zero failed modules) — drift is its own axis.
func TestUpdateRunStatus_DetectsDrift(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{
		ID:                "e1",
		Hostname:          "e1.example.com",
		ConvergenceStatus: "no data",
	}, nil)

	now := time.Now()

	// Run 1: success with changes → Changed (less than DivergedThreshold).
	fi.UpdateRunStatus("e1", "r1", "success", 1, 0, 5, now)
	if got := fi.GetEnvoy("e1"); got.ConvergenceStatus != "converged" {
		t.Errorf("after run 1: ConvergenceStatus = %q, want converged", got.ConvergenceStatus)
	}
	if got := fi.GetEnvoy("e1"); got.Drift != convergence.DriftChanged {
		t.Errorf("after run 1: Drift = %d, want DriftChanged", got.Drift)
	}

	// Runs 2-4: still under threshold (need 5 consecutive).
	for i, ts := range []time.Time{now.Add(time.Minute), now.Add(2 * time.Minute), now.Add(3 * time.Minute)} {
		runID := []string{"r2", "r3", "r4"}[i]
		fi.UpdateRunStatus("e1", runID, "success", 1, 0, 5, ts)
	}
	if got := fi.GetEnvoy("e1"); got.Drift != convergence.DriftChanged {
		t.Errorf("after 4 consecutive: Drift = %d, want DriftChanged (still under threshold)", got.Drift)
	}

	// Run 5: hits DivergedThreshold — flips to Diverged.
	fi.UpdateRunStatus("e1", "r5", "success", 1, 0, 5, now.Add(4*time.Minute))
	if got := fi.GetEnvoy("e1"); got.Drift != convergence.DriftDiverged {
		t.Errorf("after run 5: Drift = %d, want DriftDiverged", got.Drift)
	}
	if got := fi.GetEnvoy("e1"); got.ConvergenceStatus != "converged" {
		t.Errorf("after run 5: ConvergenceStatus = %q, want converged (drift is orthogonal)", got.ConvergenceStatus)
	}

	// Run 6: clean run resets the drift counter back to None.
	fi.UpdateRunStatus("e1", "r6", "success", 0, 0, 5, now.Add(5*time.Minute))
	if got := fi.GetEnvoy("e1"); got.Drift != convergence.DriftNone {
		t.Errorf("after run 6: Drift = %d, want DriftNone", got.Drift)
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
