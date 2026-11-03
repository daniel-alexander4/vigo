// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package license

import "testing"

func TestTierForNodes(t *testing.T) {
	cases := []struct {
		nodes int
		want  string
	}{
		{1, "Community"},
		{100, "Community"},
		{101, "Starter"},
		{250, "Starter"},
		{251, "Growth"},
		{1000, "Growth"},
		{1001, "Business"},
		{5000, "Business"},
		{5001, "Enterprise"},
		{25000, "Enterprise"},
		{25001, "Scale"},
		{100000, "Scale"},
		{500000, "Scale"}, // beyond published ceiling falls through to Scale
	}
	for _, tc := range cases {
		got := TierForNodes(tc.nodes)
		if got == nil {
			t.Errorf("TierForNodes(%d) = nil, want %q", tc.nodes, tc.want)
			continue
		}
		if got.Name != tc.want {
			t.Errorf("TierForNodes(%d) = %q, want %q", tc.nodes, got.Name, tc.want)
		}
	}
}

func TestTiersOrderedAndContiguous(t *testing.T) {
	for i := 1; i < len(Tiers); i++ {
		prev := Tiers[i-1]
		cur := Tiers[i]
		if cur.MinNodes != prev.MaxNodes+1 {
			t.Errorf("tier %q starts at %d, expected %d (end of %q + 1)",
				cur.Name, cur.MinNodes, prev.MaxNodes+1, prev.Name)
		}
	}
}

func TestBracketBill(t *testing.T) {
	cases := []struct {
		nodes int
		want  int
	}{
		{0, 0},
		{-5, 0},
		{1, 0},
		{100, 0},
		{101, 144},                    // first paid node
		{200, 14400},                  // 100 × $144
		{250, 21600},                  // 150 × $144
		{251, 21732},                  // 21600 + 1 × $132
		{500, 54600},                  // 21600 + 250 × $132
		{1000, 120600},                // 21600 + 99000
		{1001, 120720},                // 120600 + 1 × $120
		{5000, 600600},                // 21600 + 99000 + 480000
		{5001, 600708},                // 600600 + 1 × $108
		{25000, 2760600},              // 21600 + 99000 + 480000 + 2160000
		{25001, 2760696},              // 2760600 + 1 × $96
		{100000, 9960600},             // bracket sum at top of Scale
		{200000, 19560600},            // beyond Scale ceiling: extrapolate at $96
	}
	for _, tc := range cases {
		got := BracketBill(tc.nodes)
		if got != tc.want {
			t.Errorf("BracketBill(%d) = %d, want %d", tc.nodes, got, tc.want)
		}
	}
}

// TestBracketBillMonotonic asserts the bill never decreases as the
// fleet grows. Adding a node may cost zero (in the Community bracket)
// but must never reduce the bill — that would be a regressive cliff,
// the exact failure mode graduated brackets is meant to eliminate.
func TestBracketBillMonotonic(t *testing.T) {
	prev := -1
	for n := 0; n <= 30000; n++ {
		got := BracketBill(n)
		if got < prev {
			t.Fatalf("BracketBill(%d) = %d, less than BracketBill(%d) = %d (regressive cliff)",
				n, got, n-1, prev)
		}
		prev = got
	}
}
