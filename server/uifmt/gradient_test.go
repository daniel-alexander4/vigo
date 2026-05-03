// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package uifmt

import "testing"

func TestFailureGradientWaypoints(t *testing.T) {
	cases := []struct {
		pct  float64
		want string
		why  string
	}{
		{0, HexGreen, "0% — full green"},
		{-10, HexGreen, "negative clamps to green"},
		{40, HexYellow, "40% — full yellow waypoint"},
		{80, HexRed, "80% — full red, gradient saturates"},
		{100, HexRed, "above 80% stays red"},
		{200, HexRed, "way above clamps to red"},
	}
	for _, tc := range cases {
		if got := FailureGradient(tc.pct); got != tc.want {
			t.Errorf("%s: FailureGradient(%v) = %q, want %q", tc.why, tc.pct, got, tc.want)
		}
	}
}

func TestFailureGradientInterpolation(t *testing.T) {
	// Mid-points between waypoints must NOT equal either endpoint.
	if got := FailureGradient(20); got == HexGreen || got == HexYellow {
		t.Errorf("FailureGradient(20) = %q; expected an interpolated color between green and yellow", got)
	}
	if got := FailureGradient(60); got == HexYellow || got == HexRed {
		t.Errorf("FailureGradient(60) = %q; expected an interpolated color between yellow and red", got)
	}

	// Output must always be a valid #rrggbb string.
	for _, pct := range []float64{0, 10, 25, 40, 55, 70, 80, 100} {
		got := FailureGradient(pct)
		if len(got) != 7 || got[0] != '#' {
			t.Errorf("FailureGradient(%v) = %q; expected #rrggbb format", pct, got)
		}
	}
}
