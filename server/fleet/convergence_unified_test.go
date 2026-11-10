// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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
		configcrates int64
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
		{"blocked run with one blocked resource of 5 is degraded", "blocked", 0, 1, 5, "degraded"},
		{"blocked run with every resource blocked is failed", "blocked", 0, 5, 5, "failed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fi := NewFleetIndex()
			fi.AddEnvoy(EnvoyState{
				ID:                "e1",
				Hostname:          "e1.example.com",
				ConvergenceStatus: "no data",
			}, nil)

			fi.UpdateRunStatus("e1", "run-1", tc.runStatus, tc.changed, tc.failed, tc.configcrates, time.Now())

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

// TestAddEnvoy_InitializesDriftCountersFromState pins the rehydrate
// invariant: an envoy added with a non-None Drift contributes to the
// fleet drift counters immediately, without waiting for the next
// UpdateRunStatus call. Before this was wired, Rehydrate booted envoys
// with the correct RecentRuns history but ts.Drift = DriftNone and
// counter = 0 — the row pill and chip both read "no drift" until the
// next agent report.
func TestAddEnvoy_InitializesDriftCountersFromState(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{
		ID:                "e1",
		Hostname:          "e1.example.com",
		ConvergenceStatus: "degraded",
		Drift:             convergence.DriftDiverged,
	}, nil)

	c := fi.ConvergenceSummary()
	// Diverged ⊆ Changed: the Diverged envoy counts toward both buckets.
	if c.Diverged != 1 {
		t.Errorf("Diverged counter = %d, want 1", c.Diverged)
	}
	if c.Changed != 1 {
		t.Errorf("Changed counter = %d, want 1 (Diverged ⊆ Changed)", c.Changed)
	}
}

// TestAddEnvoy_RevokedDoesNotContribute pins that a revoked envoy added
// at boot doesn't pollute the drift counters even if its stored Drift
// is non-None — only active envoys count toward fleet buckets.
func TestAddEnvoy_RevokedDoesNotContribute(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{
		ID:                "e1",
		Hostname:          "e1.example.com",
		ConvergenceStatus: "degraded",
		Drift:             convergence.DriftDiverged,
		Revoked:           true,
	}, nil)

	c := fi.ConvergenceSummary()
	if c.Diverged != 0 || c.Changed != 0 {
		t.Errorf("revoked envoy contributed to drift counters: Diverged=%d, Changed=%d", c.Diverged, c.Changed)
	}
}

// TestRemoveEnvoy_DecrementsDriftCounters pins that deleting an envoy
// from the index releases its drift contribution. Otherwise the chip
// counts would over-report after envoy decommissioning.
func TestRemoveEnvoy_DecrementsDriftCounters(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{
		ID:                "e1",
		Hostname:          "e1.example.com",
		ConvergenceStatus: "degraded",
		Drift:             convergence.DriftDiverged,
	}, nil)

	fi.RemoveEnvoy("e1")

	c := fi.ConvergenceSummary()
	if c.Diverged != 0 || c.Changed != 0 {
		t.Errorf("RemoveEnvoy left drift counters non-zero: Diverged=%d, Changed=%d", c.Diverged, c.Changed)
	}
}

// TestSetRevoked_TogglesDriftCounters pins that revoke/un-revoke
// toggles the drift contribution symmetrically with the failure-axis
// counters. Revoking a Diverged envoy must drop both Changed and
// Diverged by 1; un-revoking must restore them.
func TestSetRevoked_TogglesDriftCounters(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{
		ID:                "e1",
		Hostname:          "e1.example.com",
		ConvergenceStatus: "degraded",
		Drift:             convergence.DriftDiverged,
	}, nil)

	fi.SetRevoked("e1", true)
	c := fi.ConvergenceSummary()
	if c.Diverged != 0 || c.Changed != 0 {
		t.Errorf("after revoke: Diverged=%d, Changed=%d, want both 0", c.Diverged, c.Changed)
	}

	fi.SetRevoked("e1", false)
	c = fi.ConvergenceSummary()
	if c.Diverged != 1 || c.Changed != 1 {
		t.Errorf("after un-revoke: Diverged=%d, Changed=%d, want both 1", c.Diverged, c.Changed)
	}
}
