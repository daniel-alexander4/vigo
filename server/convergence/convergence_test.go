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
		{"no data when no runs", nil, "no data"},
		{"converged zero changes", []RunInfo{{Status: "success", ChangedCount: 0}}, "converged"},
		{"converged single run with changes", []RunInfo{{Status: "success", ChangedCount: 3}}, "converged"},
		{"relapsed two runs with changes", []RunInfo{{Status: "success", ChangedCount: 2}, {Status: "success", ChangedCount: 1}}, "relapsed"},
		{"diverged 3 consecutive", []RunInfo{{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 2}, {Status: "success", ChangedCount: 1}}, "diverged"},
		{"relapsed if first 2 had changes but 3rd zero", []RunInfo{{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 2}, {Status: "success", ChangedCount: 0}}, "relapsed"},
		{"blocked maps to diverged", []RunInfo{{Status: "blocked"}}, "diverged"},
		{"no data for unknown status", []RunInfo{{Status: "running"}}, "no data"},

		// Percentage-based degraded/failed thresholds.
		{"degraded 1/59 (1.7%)", []RunInfo{{Status: "partial", FailedCount: 1, ModuleCount: 59}}, "degraded"},
		{"degraded 47/59 (79.7%)", []RunInfo{{Status: "partial", FailedCount: 47, ModuleCount: 59}}, "degraded"},
		{"failed 48/59 (81.4%)", []RunInfo{{Status: "partial", FailedCount: 48, ModuleCount: 59}}, "failed"},
		{"failed 59/59 (100%)", []RunInfo{{Status: "failure", FailedCount: 59, ModuleCount: 59}}, "failed"},
		{"failed 4/5 (80%)", []RunInfo{{Status: "partial", FailedCount: 4, ModuleCount: 5}}, "failed"},
		{"degraded 3/5 (60%)", []RunInfo{{Status: "partial", FailedCount: 3, ModuleCount: 5}}, "degraded"},
		{"converged 0/59", []RunInfo{{Status: "success", FailedCount: 0, ModuleCount: 59}}, "converged"},

		// Legacy: failure status without module counts falls back to "failed".
		{"failed on failure without counts", []RunInfo{{Status: "failure"}}, "failed"},
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
	statuses := []string{"converged", "converged", "relapsed", "diverged", "failed", "no data", "converged", "degraded"}
	got := Summary(statuses)
	want := map[string]int32{"converged": 3, "relapsed": 1, "diverged": 1, "failed": 1, "no data": 1, "degraded": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Summary()[%q] = %d, want %d", k, got[k], v)
		}
	}
}

func TestComputeConvergenceEmptyStatus(t *testing.T) {
	got := ComputeConvergence([]RunInfo{{Status: ""}})
	if got != "no data" {
		t.Errorf("empty status: got %q, want no data", got)
	}
}
