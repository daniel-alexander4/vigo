// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

func TestHostMatchesPatterns(t *testing.T) {
	cases := []struct {
		name     string
		hostname string
		patterns []string
		want     bool
	}{
		// Empty list → default-deny.
		{"empty list denies", "annlap", nil, false},
		{"empty slice denies", "annlap", []string{}, false},

		// Bare wildcard allows everything.
		{"star allows", "annlap", []string{"*"}, true},
		{"star allows another", "rasp1", []string{"*"}, true},

		// First-match-wins: deny before star.
		{"deny then star: denied host", "rasp1", []string{"-rasp1", "*"}, false},
		{"deny then star: allowed host", "annlap", []string{"-rasp1", "*"}, true},

		// First-match-wins: allow before deny — once allowed, the later
		// deny does not apply.
		{"allow before deny: positive wins", "annlap", []string{"annlap", "-annlap"}, true},

		// Explicit allow set, no wildcard → only listed hosts allowed.
		{"explicit set: in", "annlap", []string{"annlap", "plex"}, true},
		{"explicit set: in 2", "plex", []string{"annlap", "plex"}, true},
		{"explicit set: out", "danlap", []string{"annlap", "plex"}, false},

		// Globs.
		{"glob match", "web1.prod.example.com", []string{"*.prod.example.com"}, true},
		{"glob no match", "web1.dev.example.com", []string{"*.prod.example.com"}, false},
		{"deny glob over star: denied", "test1.test.example.com", []string{"-*.test.example.com", "*"}, false},
		{"deny glob over star: allowed", "web1.prod.example.com", []string{"-*.test.example.com", "*"}, true},

		// Question-mark glob.
		{"single-char glob", "rasp1", []string{"rasp?"}, true},
		{"single-char glob no", "raspberry", []string{"rasp?"}, false},

		// Empty / malformed entries skipped.
		{"empty entries skipped", "annlap", []string{"", "-", "annlap"}, true},
		{"only empty entries deny", "annlap", []string{"", "-", ""}, false},
		{"malformed pattern skipped", "annlap", []string{"[", "annlap"}, true},

		// Default-deny on no match.
		{"no match deny only", "annlap", []string{"-rasp1"}, false},
		{"no match positive only", "rasp1", []string{"annlap"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HostMatchesPatterns(tc.hostname, tc.patterns)
			if got != tc.want {
				t.Errorf("HostMatchesPatterns(%q, %v) = %v, want %v",
					tc.hostname, tc.patterns, got, tc.want)
			}
		})
	}
}
