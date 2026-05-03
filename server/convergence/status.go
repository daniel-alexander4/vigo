// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package convergence

import "strings"

// Status is the canonical failure-axis bucket for an envoy or run.
// Reachability (online/offline/stale) lives in server/freshness; drift
// (mutation pattern across recent runs) lives in DriftLevel below.
// Status carries failure severity only.
type Status int

const (
	StatusNoData    Status = iota // no run data available
	StatusConverged               // last run had zero failures
	StatusDegraded                // 1-79% of modules failed in the last run
	StatusFailed                  // last run failed outright (>=80% failures, or status="blocked")
)

// String returns the canonical lowercase form. Matches the strings
// ComputeConvergence returns, so callers comparing String() output
// against database / wire vocabulary stay correct.
func (s Status) String() string {
	switch s {
	case StatusConverged:
		return "converged"
	case StatusDegraded:
		return "degraded"
	case StatusFailed:
		return "failed"
	default:
		return "no data"
	}
}

// ClassifyStatus parses a failure-axis status string into the canonical
// bucket. Unknown vocabularies land in StatusNoData. Whitespace and
// case tolerant.
func ClassifyStatus(s string) Status {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "converged":
		return StatusConverged
	case "degraded":
		return StatusDegraded
	case "failed":
		return StatusFailed
	default:
		return StatusNoData
	}
}

// ComputeStatus derives the failure-axis Status for a single run.
// Equivalent to ClassifyStatus(ComputeConvergence([]RunInfo{ri})) but
// expresses the per-run case directly.
func ComputeStatus(ri RunInfo) Status {
	return ClassifyStatus(ComputeConvergence([]RunInfo{ri}))
}

// DriftLevel is the canonical drift-axis bucket. Mutually exclusive
// with itself, orthogonal to Status — an envoy can be Converged AND
// Diverged simultaneously (zero failures but modules keep mutating).
type DriftLevel int

const (
	DriftNone     DriftLevel = iota // last run had no changes
	DriftChanged                    // last run had changes; fewer than DivergedThreshold consecutive
	DriftDiverged                   // DivergedThreshold or more consecutive runs with changes
)

// String returns the canonical lowercase form. "none" is the absence
// of drift; "changed" is the broad bucket; "diverged" is the persistent
// sub-bucket.
func (d DriftLevel) String() string {
	switch d {
	case DriftChanged:
		return "changed"
	case DriftDiverged:
		return "diverged"
	default:
		return "none"
	}
}

// ClassifyDrift parses a drift-axis label string into the canonical
// bucket. Whitespace and case tolerant.
func ClassifyDrift(s string) DriftLevel {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "changed":
		return DriftChanged
	case "diverged":
		return DriftDiverged
	default:
		return DriftNone
	}
}
