// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
