// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package convergence computes convergence status for managed envoys based on
// their last run results. Reachability (online/offline) is tracked separately.
package convergence


// RunInfo holds the minimal information needed to compute convergence status.
// Decoupled from db.Run to avoid package coupling.
type RunInfo struct {
	Status       string
	ChangedCount int64
	FailedCount  int64
	ModuleCount  int64
}

const (
	// RelapsedThreshold is the number of consecutive runs with changes
	// before an envoy is flagged as "relapsed" (drifted back and fixed again).
	RelapsedThreshold = 2

	// DivergedThreshold is the number of consecutive runs with changes
	// before an envoy is flagged as "diverged" (persistent conflict).
	DivergedThreshold = 3

	// DegradedMaxPct is the upper bound (exclusive) for "degraded" status.
	// Failure percentage below this = degraded, at or above = failed.
	DegradedMaxPct = 80

	// DegradedMinPct is the lower bound (inclusive) for "degraded" status.
	// Failure percentage below this = converged (too few failures to flag).
	DegradedMinPct = 1
)

// ComputeConvergence determines the convergence status for a single envoy.
// Returns one of: "converged", "relapsed", "diverged", "degraded", "failed", "no data".
//
// recentRuns should be the last N runs in reverse chronological order (newest first).
// Only the first DivergedThreshold entries are examined for drift detection.
// Reachability (online/offline) is tracked separately by the fleet index.
//
// Priority order:
//  1. No Data — no run data available
//  2. Failed — >=80% of modules failed in the last run
//  3. Degraded — 1-79% of modules failed in the last run
//  4. Blocked — resources blocked by infrastructure issues
//  5. Diverged — last 3+ consecutive runs all had changes
//  6. Relapsed — last 2 consecutive runs had changes
//  7. Converged — last run succeeded with 0% failures
func ComputeConvergence(recentRuns []RunInfo) string {
	if len(recentRuns) == 0 {
		return "no data"
	}

	last := recentRuns[0]

	// If the agent reports the run as an outright failure, trust that —
	// the agent's overall-status flag is authoritative even when the
	// per-module failure percentage is small. Without this short-circuit,
	// a run with status="failure" but only one failed module out of
	// hundreds (failure_pct below DegradedMinPct) would fall through every
	// branch and land in "no data", which then propagates to the dashboard
	// and the envoys-page pills as a misclassification.
	if last.Status == "failure" {
		return "failed"
	}

	// Check failure percentage next (takes priority over drift detection
	// for runs the agent reports as success/partial).
	if last.FailedCount > 0 && last.ModuleCount > 0 {
		pct := float64(last.FailedCount) / float64(last.ModuleCount) * 100
		if pct >= DegradedMaxPct {
			return "failed"
		}
		if pct >= DegradedMinPct {
			return "degraded"
		}
	}

	switch last.Status {
	case "blocked":
		return "diverged"
	case "success", "partial":
		if last.ChangedCount == 0 && last.FailedCount == 0 {
			return "converged"
		}
		// Count consecutive runs with changes for drift detection.
		consecutive := 0
		for i := 0; i < len(recentRuns); i++ {
			r := recentRuns[i]
			if (r.Status != "success" && r.Status != "partial") || (r.ChangedCount == 0 && r.FailedCount == 0) {
				break
			}
			consecutive++
		}
		if consecutive >= DivergedThreshold {
			return "diverged"
		}
		if consecutive >= RelapsedThreshold {
			return "relapsed"
		}
		return "converged"
	default:
		return "no data"
	}
}

// FailurePct computes the failure percentage for a run (0-100).
// Returns 0 if ModuleCount is 0.
func FailurePct(failedCount, moduleCount int64) float64 {
	if moduleCount <= 0 {
		return 0
	}
	return float64(failedCount) / float64(moduleCount) * 100
}

// Summary aggregates a slice of status strings into counts by status.
func Summary(statuses []string) map[string]int32 {
	counts := make(map[string]int32)
	for _, s := range statuses {
		counts[s]++
	}
	return counts
}
