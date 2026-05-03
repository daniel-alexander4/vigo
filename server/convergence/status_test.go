// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package convergence

import "testing"

func TestStatusString(t *testing.T) {
	cases := []struct {
		s    Status
		want string
	}{
		{StatusConverged, "converged"},
		{StatusRelapsed, "relapsed"},
		{StatusDiverged, "diverged"},
		{StatusDegraded, "degraded"},
		{StatusFailed, "failed"},
		{StatusNoData, "no data"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("Status(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		in   string
		want Status
	}{
		{"converged", StatusConverged},
		{"  CONVERGED  ", StatusConverged},
		{"relapsed", StatusRelapsed},
		{"diverged", StatusDiverged},
		{"degraded", StatusDegraded},
		{"failed", StatusFailed},
		{"", StatusNoData},
		{"no data", StatusNoData},
		{"unknown", StatusNoData},
		{"online", StatusNoData},  // reachability vocabulary stays out
		{"offline", StatusNoData}, // reachability vocabulary stays out
	}
	for _, tc := range cases {
		if got := ClassifyStatus(tc.in); got != tc.want {
			t.Errorf("ClassifyStatus(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestClassifyStatusRoundTrip(t *testing.T) {
	// Every Status value's String() must round-trip back through ClassifyStatus.
	for _, s := range []Status{StatusConverged, StatusRelapsed, StatusDiverged, StatusDegraded, StatusFailed} {
		if got := ClassifyStatus(s.String()); got != s {
			t.Errorf("round-trip broken: %d -> %q -> %d", s, s.String(), got)
		}
	}
}

func TestComputeStatus(t *testing.T) {
	cases := []struct {
		name string
		ri   RunInfo
		want Status
	}{
		{"clean success", RunInfo{Status: "success", ChangedCount: 0, FailedCount: 0, ModuleCount: 10}, StatusConverged},
		{"agent failure with low fail pct -> degraded by counts", RunInfo{Status: "failure", FailedCount: 1, ModuleCount: 100}, StatusDegraded},
		{"degraded", RunInfo{Status: "success", FailedCount: 5, ModuleCount: 10}, StatusDegraded},
		{"empty", RunInfo{}, StatusNoData},
	}
	for _, tc := range cases {
		if got := ComputeStatus(tc.ri); got != tc.want {
			t.Errorf("%s: ComputeStatus = %d, want %d", tc.name, got, tc.want)
		}
	}
}
