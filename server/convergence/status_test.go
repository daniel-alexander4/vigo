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
		{"degraded", StatusDegraded},
		{"failed", StatusFailed},
		{"", StatusNoData},
		{"no data", StatusNoData},
		{"unknown", StatusNoData},
		{"online", StatusNoData},
		{"offline", StatusNoData},
		{"relapsed", StatusNoData},  // legacy vocabulary, dropped
		{"diverged", StatusNoData},  // belongs to drift axis now
	}
	for _, tc := range cases {
		if got := ClassifyStatus(tc.in); got != tc.want {
			t.Errorf("ClassifyStatus(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestClassifyStatusRoundTrip(t *testing.T) {
	for _, s := range []Status{StatusConverged, StatusDegraded, StatusFailed} {
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
		{"clean success", RunInfo{Status: "success", ChangedCount: 0, FailedCount: 0, ConfigcrateCount: 10}, StatusConverged},
		{"agent failure with low fail pct -> degraded by counts", RunInfo{Status: "failure", FailedCount: 1, ConfigcrateCount: 100}, StatusDegraded},
		{"degraded", RunInfo{Status: "success", FailedCount: 5, ConfigcrateCount: 10}, StatusDegraded},
		{"empty", RunInfo{}, StatusNoData},
		{"blocked is failed", RunInfo{Status: "blocked"}, StatusFailed},
	}
	for _, tc := range cases {
		if got := ComputeStatus(tc.ri); got != tc.want {
			t.Errorf("%s: ComputeStatus = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestDriftLevelString(t *testing.T) {
	cases := []struct {
		d    DriftLevel
		want string
	}{
		{DriftNone, "none"},
		{DriftChanged, "changed"},
		{DriftDiverged, "diverged"},
	}
	for _, tc := range cases {
		if got := tc.d.String(); got != tc.want {
			t.Errorf("DriftLevel(%d).String() = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestClassifyDrift(t *testing.T) {
	cases := []struct {
		in   string
		want DriftLevel
	}{
		{"none", DriftNone},
		{"changed", DriftChanged},
		{"  CHANGED  ", DriftChanged},
		{"diverged", DriftDiverged},
		{"", DriftNone},
		{"unknown", DriftNone},
	}
	for _, tc := range cases {
		if got := ClassifyDrift(tc.in); got != tc.want {
			t.Errorf("ClassifyDrift(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
