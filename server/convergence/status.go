// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package convergence

import "strings"

// Status is the canonical convergence bucket for an envoy or run.
// Reachability (online/offline/stale) is tracked separately by
// server/freshness; this type carries module-convergence state only.
type Status int

const (
	StatusNoData    Status = iota // no run data available
	StatusConverged               // last run succeeded with 0% failures, no recent drift
	StatusRelapsed                // last 2 consecutive runs had changes
	StatusDiverged                // last 3+ consecutive runs had changes, or blocked
	StatusDegraded                // 1-79% of modules failed in the last run
	StatusFailed                  // last run failed outright (>=80% failures or agent-reported failure)
)

// String returns the canonical lowercase form. Output exactly matches
// the strings ComputeConvergence has historically returned, so callers
// that compare String() output against database/wire vocabulary stay
// correct.
func (s Status) String() string {
	switch s {
	case StatusConverged:
		return "converged"
	case StatusRelapsed:
		return "relapsed"
	case StatusDiverged:
		return "diverged"
	case StatusDegraded:
		return "degraded"
	case StatusFailed:
		return "failed"
	default:
		return "no data"
	}
}

// ClassifyStatus parses a status string into the canonical bucket.
// Unknown vocabularies land in StatusNoData. Whitespace and case
// tolerant.
func ClassifyStatus(s string) Status {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "converged":
		return StatusConverged
	case "relapsed":
		return StatusRelapsed
	case "diverged":
		return StatusDiverged
	case "degraded":
		return StatusDegraded
	case "failed":
		return StatusFailed
	default:
		return StatusNoData
	}
}

// ComputeStatus derives the convergence Status for a single run.
// Equivalent to ClassifyStatus(ComputeConvergence([]RunInfo{ri})) but
// expresses the per-run case directly. Multi-run drift detection still
// requires the full ComputeConvergence path.
func ComputeStatus(ri RunInfo) Status {
	return ClassifyStatus(ComputeConvergence([]RunInfo{ri}))
}
