// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package convergence computes convergence status for managed envoys based on
// their last run results and last-seen timestamps.
package convergence

import "time"

// RunInfo holds the minimal information needed to compute convergence status.
// Decoupled from db.Run to avoid package coupling.
type RunInfo struct {
	Status       string
	ChangedCount int64
}

const (
	// RelapsedThreshold is the number of consecutive runs with changes
	// before an envoy is flagged as "relapsed" (drifted back and fixed again).
	RelapsedThreshold = 2

	// DivergedThreshold is the number of consecutive runs with changes
	// before an envoy is flagged as "diverged" (persistent conflict).
	DivergedThreshold = 3
)

// ComputeConvergence determines the convergence status for a single envoy.
// Returns one of: "converged", "relapsed", "diverged", "errors", "lost contact", "no data".
//
// recentRuns should be the last N runs in reverse chronological order (newest first).
// Only the first DivergedThreshold entries are examined for drift detection.
//
// Priority order:
//  1. Lost Contact - envoy has not checked in within 2x base interval
//  2. No Data - no run data available
//  3. Errors - last run had failures
//  4. Diverged - last 3+ consecutive runs all had changes
//  5. Relapsed - last 2 consecutive runs had changes
//  6. Converged - last run succeeded (even if it applied changes)
func ComputeConvergence(recentRuns []RunInfo, lastSeen time.Time, baseInterval time.Duration) string {
	if time.Since(lastSeen) > 2*baseInterval {
		return "lost contact"
	}

	if len(recentRuns) == 0 {
		return "no data"
	}

	last := recentRuns[0]
	switch last.Status {
	case "failure", "partial":
		return "errors"
	case "success":
		if last.ChangedCount == 0 {
			return "converged"
		}
		// Count consecutive successful runs with changes.
		consecutive := 0
		for i := 0; i < len(recentRuns); i++ {
			r := recentRuns[i]
			if r.Status != "success" || r.ChangedCount == 0 {
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

// Summary aggregates a slice of status strings into counts by status.
func Summary(statuses []string) map[string]int32 {
	counts := make(map[string]int32)
	for _, s := range statuses {
		counts[s]++
	}
	return counts
}
