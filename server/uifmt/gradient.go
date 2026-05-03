// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package uifmt

import (
	"fmt"
	"strconv"
)

// FailureGradient returns a green→yellow→red hex color for the given
// percentage where higher is worse. The ramp:
//
//	  0%  → full green   (HexGreen)
//	 40%  → full yellow  (HexYellow)
//	 80%+ → full red     (HexRed) — saturates here so the gradient
//	         maxes out exactly at the convergence DegradedMaxPct
//	         boundary; anything past that is the same color as 80%.
//
// pct is clamped to [0, 100]. Fractional values interpolate linearly
// between waypoints. Reusable across surfaces — convergence column,
// Module Health stat card, risk score, ROOT FS, compliance coverage
// (pass `100 - coverage` since coverage is "higher is better").
func FailureGradient(pct float64) string {
	if pct <= 0 {
		return HexGreen
	}
	if pct >= 80 {
		return HexRed
	}
	if pct <= 40 {
		// 0..40 → green..yellow
		return interpolateHex(HexGreen, HexYellow, pct/40)
	}
	// 40..80 → yellow..red
	return interpolateHex(HexYellow, HexRed, (pct-40)/40)
}

// interpolateHex linearly blends two #rrggbb hex strings. t ∈ [0, 1];
// 0 returns a, 1 returns b. Returns lowercase #rrggbb.
func interpolateHex(a, b string, t float64) string {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	ar, ag, ab := parseHex(a)
	br, bg, bb := parseHex(b)
	r := ar + int(float64(br-ar)*t+0.5)
	g := ag + int(float64(bg-ag)*t+0.5)
	bl := ab + int(float64(bb-ab)*t+0.5)
	return fmt.Sprintf("#%02x%02x%02x", r, g, bl)
}

// parseHex reads a #rrggbb string into three ints. Returns (0,0,0) on
// parse error — caller never feeds it user input, only palette
// constants, so a malformed input means a code bug, not a runtime
// failure mode.
func parseHex(s string) (r, g, b int) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0
	}
	r64, _ := strconv.ParseInt(s[1:3], 16, 0)
	g64, _ := strconv.ParseInt(s[3:5], 16, 0)
	b64, _ := strconv.ParseInt(s[5:7], 16, 0)
	return int(r64), int(g64), int(b64)
}
