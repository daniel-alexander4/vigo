// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package cve

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		want SeverityLevel
	}{
		{"Critical", SevCritical},
		{"critical", SevCritical},
		{"  CRITICAL  ", SevCritical},
		{"High", SevHigh},
		{"high", SevHigh},
		{"Medium", SevMedium},
		{"MEDIUM", SevMedium},
		{"Low", SevLow},
		{"info", SevInfo},
		{"informational", SevInfo},
		{"", SevUnassigned},
		{"unknown", SevUnassigned},
		{"none", SevUnassigned},
	}
	for _, tc := range cases {
		if got := Classify(tc.in); got != tc.want {
			t.Errorf("Classify(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSeverityOrdering(t *testing.T) {
	// Numeric ordering must be monotonic so callers can compare with > / >=.
	levels := []SeverityLevel{SevUnassigned, SevInfo, SevLow, SevMedium, SevHigh, SevCritical}
	for i := 1; i < len(levels); i++ {
		if levels[i] <= levels[i-1] {
			t.Errorf("severity ordering broken at index %d: %d <= %d", i, levels[i], levels[i-1])
		}
	}
}

func TestNorm(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Critical", "critical"},
		{"  HIGH  ", "high"},
		{"medium", "medium"},
		{"", "unassigned"},
	}
	for _, tc := range cases {
		if got := Norm(tc.in); got != tc.want {
			t.Errorf("Norm(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
