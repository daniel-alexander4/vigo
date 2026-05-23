// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"strings"
	"testing"
)

func TestValidateSwarmPatternOrder(t *testing.T) {
	cases := []struct {
		name      string
		patterns  []string
		wantFlag  bool
		wantInMsg string // substring expected in the finding's Reason
	}{
		// Negative cases — well-ordered lists must produce no findings.
		{"empty list is clean", nil, false, ""},
		{"single allow is clean", []string{"*"}, false, ""},
		{"deny then star is clean", []string{"-rasp1", "*"}, false, ""},
		{"explicit allow set is clean", []string{"annlap", "plex"}, false, ""},
		{"deny glob then star is clean", []string{"-rasp*", "*"}, false, ""},
		{"deny then specific is clean", []string{"-rasp1", "annlap"}, false, ""},

		// Catch-all-before-deny — the headline silent-fail case.
		{"star before specific deny", []string{"*", "-rasp1"}, true, "bare \"*\""},
		{"star before glob deny", []string{"*", "-rasp*"}, true, "bare \"*\""},

		// Glob-shadow case — earlier non-deny glob covers a later deny.
		{"glob shadows specific deny", []string{"a*", "-annlap"}, true, "filepath.Match"},
		{"deeper glob shadows", []string{"*.test.example.com", "-foo.test.example.com"}, true, "filepath.Match"},

		// Redundant identical entries — flagged. Operator can fix by
		// dropping the duplicate; the rule can't tell intent.
		{"identical pattern shadows itself", []string{"host-a", "-host-a"}, true, "filepath.Match"},

		// Earlier deny doesn't shadow a later deny — both can fire.
		{"deny before deny is clean", []string{"-rasp1", "-rasp2"}, false, ""},

		// Bare `-` (no glob after) is malformed-but-tolerated; never
		// flag it because there's nothing to compare.
		{"bare dash is ignored", []string{"-"}, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := SwarmSection{Enabled: tc.patterns}
			findings := ValidateSwarmPatternOrder(s)

			// Filter to findings on the substrate-level list (the only
			// one populated in this test). Sub-subsystems are empty
			// so they never produce findings.
			var got []SwarmPatternFinding
			for _, f := range findings {
				if f.Subsystem == "swarm" {
					got = append(got, f)
				}
			}

			if tc.wantFlag && len(got) == 0 {
				t.Fatalf("expected a finding, got none")
			}
			if !tc.wantFlag && len(got) > 0 {
				t.Fatalf("expected no finding, got %v", got)
			}
			if tc.wantInMsg != "" && len(got) > 0 {
				if !strings.Contains(got[0].Reason, tc.wantInMsg) {
					t.Errorf("reason %q missing substring %q", got[0].Reason, tc.wantInMsg)
				}
			}
		})
	}
}

func TestValidateSwarmPatternOrder_AllSubsystems(t *testing.T) {
	// Confirm each pattern list (substrate + 6 sub-subsystems) is
	// walked. Bad pattern in each list — should produce 7 findings.
	bad := []string{"*", "-host"}
	s := SwarmSection{
		Enabled:    bad,
		Puddle:     SwarmPuddleSection{Enabled: bad},
		Gitback:    GitbackSection{Enabled: bad},
		Curator:    CuratorSection{Enabled: bad},
		Poolq:      PoolqSection{Enabled: bad},
		Longdrawer: LongdrawerSection{Enabled: bad},
		Filecast:   FilecastSection{Enabled: bad},
	}
	findings := ValidateSwarmPatternOrder(s)
	if len(findings) != 7 {
		t.Fatalf("expected one finding per subsystem (7), got %d: %v", len(findings), findings)
	}
	seen := make(map[string]bool)
	for _, f := range findings {
		seen[f.Subsystem] = true
	}
	for _, sub := range []string{"swarm", "puddle", "gitback", "curator", "poolq", "longdrawer", "filecast"} {
		if !seen[sub] {
			t.Errorf("missing finding for subsystem %q", sub)
		}
	}
}

func TestSwarmPatternFinding_String(t *testing.T) {
	f := SwarmPatternFinding{
		Subsystem: "puddle",
		Index:     2,
		Pattern:   "-rasp1",
		Reason:    "preceded by bare \"*\" which matches every host",
	}
	got := f.String()
	want := `swarm.puddle.enabled[2] = "-rasp1" is unreachable: preceded by bare "*" which matches every host`
	if got != want {
		t.Errorf("String() =\n  %q\nwant\n  %q", got, want)
	}
}
