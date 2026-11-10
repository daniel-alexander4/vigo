// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

// Package convergence computes module-convergence state for managed
// envoys. Two orthogonal axes:
//
//   - Failure axis: the agent's last run produced no failures
//     (Converged), some failures (Degraded), or many (Failed). Computed
//     by ComputeConvergence; typed as Status.
//
//   - Drift axis: the envoy's modules keep mutating across recent runs.
//     A single run with changes is Changed; DivergedThreshold or more
//     consecutive runs with changes is Diverged. Computed by
//     ComputeDrift; typed as DriftLevel.
//
// An envoy occupies one bucket on each axis simultaneously — Converged
// + Diverged is a clean envoy whose modules keep churning anyway,
// signalling non-idempotent config. Reachability (online/offline) is
// tracked separately by server/freshness.
package convergence

// RunInfo holds the minimal information needed to compute convergence
// state. Decoupled from db.Run to avoid package coupling.
type RunInfo struct {
	Status        string // run-level flag, one of the RunStatus* constants, or ""
	ChangedCount  int64
	FailedCount   int64 // errored + guardrail-blocked resources (see summarizeResults)
	ResourceCount int64
}

const (
	// DivergedThreshold is the number of consecutive runs with changes
	// before an envoy is flagged Diverged on the drift axis. 5 leaves
	// enough headroom that a single bad config push doesn't immediately
	// flip operators to "drift" while still surfacing genuine
	// non-idempotent loops.
	DivergedThreshold = 5

	// DegradedMaxPct is the upper bound (exclusive) for Degraded on the
	// failure axis. Failure pct below this is Degraded; at or above is
	// Failed. There is no minimum — any FailedCount > 0 lands in Degraded
	// (or Failed if the percentage clears the upper bound). A single
	// failed module of 172 is still a failure on the dashboard.
	DegradedMaxPct = 80
)

// ComputeConvergence returns the failure-axis bucket for a single
// envoy as a canonical lowercase string. One of: "converged",
// "degraded", "failed", "no data".
//
// recentRuns must be in reverse chronological order (newest first); only
// the first entry is consulted — drift across history is the drift
// axis's job, not this function's.
//
// Rules, first match wins:
//  1. No runs                                        → "no data"
//  2. last.FailedCount == 0 with a recognized status → "converged"
//  3. failure pct >= DegradedMaxPct                  → "failed"
//  4. otherwise (any FailedCount > 0)                → "degraded"
//
// A guardrail-blocked resource is a failed resource: the ingest path counts it
// into FailedCount, so a run with one blocked resource of 172 is Degraded, not
// Failed — the same resource-granular split as an errored resource. The
// run-level "blocked" status only says why resources failed.
func ComputeConvergence(recentRuns []RunInfo) string {
	if len(recentRuns) == 0 {
		return "no data"
	}

	last := recentRuns[0]

	// Zero failures is converged regardless of changes. Drift is
	// tracked separately on the drift axis (see ComputeDrift).
	if last.FailedCount == 0 {
		switch last.Status {
		case RunStatusSuccess, RunStatusPartial, RunStatusFailure:
			return "converged"
		}
		// Empty / unknown status with zero counts → "no data"; this is
		// not "converged at zero," it's "we don't know." A "blocked"
		// run always carries its blocked resources in FailedCount, so
		// blocked with zero failures is an inconsistent row, not converged.
		return "no data"
	}

	// Failure percentage classification. Any FailedCount > 0 is at
	// least Degraded; >= DegradedMaxPct upgrades to Failed.
	if last.ResourceCount > 0 {
		pct := float64(last.FailedCount) / float64(last.ResourceCount) * 100
		if pct >= DegradedMaxPct {
			return "failed"
		}
		return "degraded"
	}

	// FailedCount > 0 but ResourceCount == 0 — degenerate input; treat
	// as a real failure rather than ignoring the non-zero failure count.
	return "failed"
}

// ComputeDrift returns the drift-axis bucket for a single envoy. One of
// DriftNone (last run made no changes) / DriftChanged (last run made
// changes; fewer than DivergedThreshold consecutive) / DriftDiverged
// (DivergedThreshold or more consecutive runs with changes).
//
// recentRuns must be in reverse chronological order (newest first).
// Counts purely on ChangedCount > 0; failure counts are the failure
// axis's job, not this function's.
func ComputeDrift(recentRuns []RunInfo) DriftLevel {
	if len(recentRuns) == 0 || recentRuns[0].ChangedCount == 0 {
		return DriftNone
	}
	consecutive := 0
	for _, r := range recentRuns {
		if r.ChangedCount == 0 {
			break
		}
		consecutive++
	}
	if consecutive >= DivergedThreshold {
		return DriftDiverged
	}
	return DriftChanged
}

// FailurePct computes the failure percentage for a run (0-100).
// Returns 0 if ResourceCount is 0.
func FailurePct(failedCount, resourceCount int64) float64 {
	if resourceCount <= 0 {
		return 0
	}
	return float64(failedCount) / float64(resourceCount) * 100
}

// ConvergedPct computes the convergence percentage over the ACTIVE fleet
// (0-100): converged / active. Returns 0 when active is 0. The 2-arg shape
// mirrors FailurePct; the caller supplies active (= total − offline, since
// offline envoys are powered-off/decommissioned, not health problems) so every
// surface divides by the same denominator. This is the single source of truth
// for the percentage — the dashboard once divided by raw total and disagreed
// with the convergence page, the sparkline, and the alerter.
func ConvergedPct(converged, active int64) float64 {
	if active <= 0 {
		return 0
	}
	return float64(converged) / float64(active) * 100
}

// Summary aggregates a slice of status strings into counts by status.
func Summary(statuses []string) map[string]int32 {
	counts := make(map[string]int32)
	for _, s := range statuses {
		counts[s]++
	}
	return counts
}
