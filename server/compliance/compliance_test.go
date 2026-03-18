// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package compliance

import (
	"testing"
	"time"
)

func TestComputeStatus(t *testing.T) {
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
		{"compliant zero changes", []RunInfo{{Status: "success", ChangedCount: 0}}, now, "compliant"},
		{"compliant single run with changes", []RunInfo{{Status: "success", ChangedCount: 3}}, now, "compliant"},
		{"compliant two runs with changes", []RunInfo{{Status: "success", ChangedCount: 2}, {Status: "success", ChangedCount: 1}}, now, "compliant"},
		{"recurring drift 3 consecutive", []RunInfo{{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 2}, {Status: "success", ChangedCount: 1}}, now, "recurring drift"},
		{"not recurring if 3rd had zero", []RunInfo{{Status: "success", ChangedCount: 1}, {Status: "success", ChangedCount: 2}, {Status: "success", ChangedCount: 0}}, now, "compliant"},
		{"errors on failure", []RunInfo{{Status: "failure"}}, now, "errors"},
		{"errors on partial", []RunInfo{{Status: "partial", ChangedCount: 1}}, now, "errors"},
		{"no data for unknown status", []RunInfo{{Status: "running"}}, now, "no data"},
		{"lost contact priority", []RunInfo{{Status: "success"}}, now.Add(-2 * time.Hour), "lost contact"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeStatus(tt.runs, tt.last, baseInterval)
			if got != tt.want {
				t.Errorf("ComputeStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	statuses := []string{"compliant", "compliant", "recurring drift", "errors", "lost contact", "no data", "compliant"}
	got := Summary(statuses)
	want := map[string]int32{"compliant": 3, "recurring drift": 1, "errors": 1, "lost contact": 1, "no data": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Summary()[%q] = %d, want %d", k, got[k], v)
		}
	}
}

func TestComputeStatusBoundary(t *testing.T) {
	base := 5 * time.Minute
	staleAt := time.Now().Add(-2*base - time.Millisecond)
	got := ComputeStatus([]RunInfo{{Status: "success"}}, staleAt, base)
	if got != "lost contact" {
		t.Errorf("at boundary: got %q, want lost contact", got)
	}
	freshAt := time.Now().Add(-2*base + time.Second)
	got = ComputeStatus([]RunInfo{{Status: "success"}}, freshAt, base)
	if got != "compliant" {
		t.Errorf("inside boundary: got %q, want compliant", got)
	}
}

func TestComputeStatusZeroInterval(t *testing.T) {
	got := ComputeStatus([]RunInfo{{Status: "success"}}, time.Now().Add(-time.Millisecond), 0)
	if got != "lost contact" {
		t.Errorf("zero interval: got %q, want lost contact", got)
	}
}

func TestComputeStatusEmptyStatus(t *testing.T) {
	got := ComputeStatus([]RunInfo{{Status: ""}}, time.Now(), 5*time.Minute)
	if got != "no data" {
		t.Errorf("empty status: got %q, want no data", got)
	}
}
