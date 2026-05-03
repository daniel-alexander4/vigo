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

	// Trust per-module counts over the agent's overall-status flag when
	// the agent actually reported a recognized run status. A run with
	// zero failed modules and zero changes is converged even if
	// last.Status == "failure" — the overall flag is a coarser signal
	// that occasionally lies (executor exits non-zero without populating
	// per-module results, producing a status="failure" run with
	// FailedCount=0). The dashboard treats module counts as the truth so
	// those bookkeeping anomalies don't contaminate the rollup.
	//
	// Empty / unknown status with zero counts still falls through to
	// "no data" — that's not "converged at zero," it's "we don't know."
	if last.FailedCount == 0 && last.ChangedCount == 0 {
		switch last.Status {
		case "success", "partial", "failure":
			return "converged"
		}
		// "blocked" is intentionally absent: an infrastructure block is
		// a real divergence even when the run produced zero counts,
		// because no resources got applied. Falls through to the switch
		// below which maps blocked → diverged.
	}

	// Check failure percentage next.
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
	case "success", "partial", "failure":
		// Drift detection for runs with changes below the degraded
		// threshold. "failure" lands here too when FailedCount is small
		// — module counts already determined we aren't in failed/degraded,
		// so we look at change history just like a success/partial run.
		consecutive := 0
		for i := 0; i < len(recentRuns); i++ {
			r := recentRuns[i]
			if r.ChangedCount == 0 && r.FailedCount == 0 {
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
