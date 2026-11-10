// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

import "testing"

func TestVersionGE(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// The lexical-compare bug: "9" >= "10" must be false, not true.
		{"10", "9", true},
		{"9", "10", false},
		{"9", "9", true},
		{"9.5", "9", true},
		{"9", "9.5", false},
		{"9.10", "9.5", true},  // lexical would say "9.10" < "9.5"
		{"9.5", "9.10", false}, // and the inverse
		{"22.04", "9", true},   // lexical would say "22.04" < "9"
		{"22.04", "24.04", false},
		{"9", "9.0", true}, // missing component treated as 0
		{"9.0", "9", true},
		// Platform suffixes: leading digits parsed, suffix ignored.
		{"14.0-RELEASE", "13.2-RELEASE", true},
		{"14.0-RELEASE", "14.0", true},
		{"10.0.22631", "10.0.19045", true},
		// No leading digits compare as 0.
		{"", "0", true},
		{"unknown", "1", false},
	}
	for _, c := range cases {
		if got := versionGE(c.a, c.b); got != c.want {
			t.Errorf("versionGE(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
