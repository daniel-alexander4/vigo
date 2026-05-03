// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package convergence

import "testing"

func TestComputeConvergence(t *testing.T) {
	tests := []struct {
		name string
		runs []RunInfo
		want string
	}{
		// Edge cases.
		{"no data when no runs", nil, "no data"},
		{"unknown status with zero counts", []RunInfo{{Status: ""}}, "no data"},
		{"unknown status with zero counts (running)", []RunInfo{{Status: "running"}}, "no data"},

		// Failure-axis buckets — module counts decide, not the agent flag.
		{"clean success", []RunInfo{{Status: "success", ChangedCount: 0, FailedCount: 0, ModuleCount: 5}}, "converged"},
		{"converged with changes — drift axis catches this, not failure", []RunInfo{{Status: "success", ChangedCount: 3, FailedCount: 0, ModuleCount: 5}}, "converged"},
		{"converged with changes across many runs", []RunInfo{
			{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 2},
			{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 1},
			{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 1},
		}, "converged"},
		{"degraded 1/172 (0.58%) — any failure is at least degraded", []RunInfo{{Status: "partial", FailedCount: 1, ModuleCount: 172}}, "degraded"},
		{"degraded 1/59 (1.7%)", []RunInfo{{Status: "partial", FailedCount: 1, ModuleCount: 59}}, "degraded"},
		{"degraded 47/59 (79.7%)", []RunInfo{{Status: "partial", FailedCount: 47, ModuleCount: 59}}, "degraded"},
		{"failed 48/59 (81.4%)", []RunInfo{{Status: "partial", FailedCount: 48, ModuleCount: 59}}, "failed"},
		{"failed 59/59 (100%)", []RunInfo{{Status: "failure", FailedCount: 59, ModuleCount: 59}}, "failed"},

		// Bookkeeping anomalies — module counts override the agent flag.
		{"failure status with zero counts is converged", []RunInfo{{Status: "failure"}}, "converged"},
		{"failure status with low fail pct is degraded by counts", []RunInfo{{Status: "failure", FailedCount: 1, ModuleCount: 100}}, "degraded"},

		// Blocked is its own special case — modules didn't apply, treat
		// as Failed on the failure axis regardless of counts.
		{"blocked maps to failed", []RunInfo{{Status: "blocked"}}, "failed"},

		// Degenerate: failed > 0 but no module count → failed (don't lose the failure signal).
		{"failed > 0 with zero modules", []RunInfo{{Status: "partial", FailedCount: 3}}, "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeConvergence(tt.runs)
			if got != tt.want {
				t.Errorf("ComputeConvergence() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComputeDrift(t *testing.T) {
	tests := []struct {
		name string
		runs []RunInfo
		want DriftLevel
	}{
		{"no runs", nil, DriftNone},
		{"latest run no changes", []RunInfo{{Status: "success", ChangedCount: 0}}, DriftNone},
		{"single changed run", []RunInfo{{Status: "success", ChangedCount: 1}}, DriftChanged},
		{"two consecutive changes — still Changed (under threshold)", []RunInfo{
			{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 1},
		}, DriftChanged},
		{"four consecutive changes — still Changed", []RunInfo{
			{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 1},
			{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 1},
		}, DriftChanged},
		{"five consecutive changes — Diverged", []RunInfo{
			{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 1},
			{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 1},
			{Status: "success", ChangedCount: 1},
		}, DriftDiverged},
		{"changes broken by clean run — back to Changed", []RunInfo{
			{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 1},
			{Status: "success", ChangedCount: 0}, {Status: "success", ChangedCount: 1},
			{Status: "success", ChangedCount: 1},
		}, DriftChanged},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeDrift(tt.runs)
			if got != tt.want {
				t.Errorf("ComputeDrift() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFailurePct(t *testing.T) {
	tests := []struct {
		failed, total int64
		wantApprox    float64
	}{
		{0, 59, 0},
		{1, 59, 1.69},
		{59, 59, 100},
		{0, 0, 0},
		{5, 0, 0},
		{48, 59, 81.35},
	}
	for _, tt := range tests {
		got := FailurePct(tt.failed, tt.total)
		diff := got - tt.wantApprox
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.1 {
			t.Errorf("FailurePct(%d, %d) = %.2f, want ~%.2f", tt.failed, tt.total, got, tt.wantApprox)
		}
	}
}

func TestSummary(t *testing.T) {
	statuses := []string{"converged", "converged", "failed", "no data", "converged", "degraded"}
	got := Summary(statuses)
	want := map[string]int32{"converged": 3, "failed": 1, "no data": 1, "degraded": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Summary()[%q] = %d, want %d", k, got[k], v)
		}
	}
}
