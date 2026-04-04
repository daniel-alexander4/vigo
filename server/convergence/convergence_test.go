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
		{"failed on failure", []RunInfo{{Status: "failure"}}, "failed"},
		{"failed on partial", []RunInfo{{Status: "partial", ChangedCount: 1}}, "failed"},
		{"blocked maps to diverged", []RunInfo{{Status: "blocked"}}, "diverged"},
		{"no data for unknown status", []RunInfo{{Status: "running"}}, "no data"},
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

func TestSummary(t *testing.T) {
	statuses := []string{"converged", "converged", "relapsed", "diverged", "failed", "no data", "converged"}
	got := Summary(statuses)
	want := map[string]int32{"converged": 3, "relapsed": 1, "diverged": 1, "failed": 1, "no data": 1}
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
