// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package convergence

import (
	"testing"
	"time"
)

func TestComputeConvergence(t *testing.T) {
	baseInterval := 5 * time.Minute
	now := time.Now()

	tests := []struct {
		name string
		runs []RunInfo
		last time.Time
		want string
	}{
		{"lost contact", []RunInfo{{Status: "success"}}, now.Add(-61 * time.Minute), "lost contact"},
		{"no data when no runs", nil, now, "no data"},
		{"compliant zero changes", []RunInfo{{Status: "success", ChangedCount: 0}}, now, "converged"},
		{"compliant single run with changes", []RunInfo{{Status: "success", ChangedCount: 3}}, now, "converged"},
		{"relapsed two runs with changes", []RunInfo{{Status: "success", ChangedCount: 2}, {Status: "success", ChangedCount: 1}}, now, "relapsed"},
		{"diverged 3 consecutive", []RunInfo{{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 2}, {Status: "success", ChangedCount: 1}}, now, "diverged"},
		{"relapsed if first 2 had changes but 3rd zero", []RunInfo{{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 2}, {Status: "success", ChangedCount: 0}}, now, "relapsed"},
		{"errors on failure", []RunInfo{{Status: "failure"}}, now, "errors"},
		{"errors on partial", []RunInfo{{Status: "partial", ChangedCount: 1}}, now, "errors"},
		{"no data for unknown status", []RunInfo{{Status: "running"}}, now, "no data"},
		{"lost contact priority", []RunInfo{{Status: "success"}}, now.Add(-2 * time.Hour), "lost contact"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeConvergence(tt.runs, tt.last, baseInterval)
			if got != tt.want {
				t.Errorf("ComputeConvergence() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	statuses := []string{"converged", "converged", "relapsed", "diverged", "errors", "lost contact", "no data", "converged"}
	got := Summary(statuses)
	want := map[string]int32{"converged": 3, "relapsed": 1, "diverged": 1, "errors": 1, "lost contact": 1, "no data": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Summary()[%q] = %d, want %d", k, got[k], v)
		}
	}
}

func TestComputeConvergenceBoundary(t *testing.T) {
	base := 5 * time.Minute
	staleAt := time.Now().Add(-2*base - time.Millisecond)
	got := ComputeConvergence([]RunInfo{{Status: "success"}}, staleAt, base)
	if got != "lost contact" {
		t.Errorf("at boundary: got %q, want lost contact", got)
	}
	freshAt := time.Now().Add(-2*base + time.Second)
	got = ComputeConvergence([]RunInfo{{Status: "success"}}, freshAt, base)
	if got != "converged" {
		t.Errorf("inside boundary: got %q, want compliant", got)
	}
}

func TestComputeConvergenceZeroInterval(t *testing.T) {
	got := ComputeConvergence([]RunInfo{{Status: "success"}}, time.Now().Add(-time.Millisecond), 0)
	if got != "lost contact" {
		t.Errorf("zero interval: got %q, want lost contact", got)
	}
}

func TestComputeConvergenceEmptyStatus(t *testing.T) {
	got := ComputeConvergence([]RunInfo{{Status: ""}}, time.Now(), 5*time.Minute)
	if got != "no data" {
		t.Errorf("empty status: got %q, want no data", got)
	}
}
