// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/modlint"
)

// TestConfigcratesLintClean asserts every generated synthetic configcrate
// passes modlint with no Error-severity findings — i.e. publish accepts
// it. Warnings are tolerated, matching the example-configs lint gate.
func TestConfigcratesLintClean(t *testing.T) {
	for name, body := range configcrateFiles {
		res := modlint.Lint([]byte(body), modlint.Options{})
		if !res.OK() {
			t.Errorf("%s has lint errors: %v", name, res.Errors)
		}
	}
}

// TestHostcratesLintClean asserts every region's generated hostcrate
// passes the hostcrate linter clean.
func TestHostcratesLintClean(t *testing.T) {
	for _, r := range synthRegions {
		body := renderHostcrate(r)
		res := modlint.LintHostcrate([]byte(body), modlint.Options{})
		if !res.OK() {
			t.Errorf("hostcrate %s/%s has lint errors: %v", r.cloud, r.name, res.Errors)
		}
	}
}

// TestHostPlanCountAndBroken verifies the plan lands the exact requested
// count and exactly the requested number of broken hosts, for both the
// default fleet size and an arbitrary one (distribution must always sum).
func TestHostPlanCountAndBroken(t *testing.T) {
	for _, tc := range []struct{ count, broken int }{
		{13647, 7},
		{1000, 5},
		{100, 0},
	} {
		plan := buildHostPlan(tc.count, tc.broken)
		if len(plan) != tc.count {
			t.Errorf("count=%d: got %d hosts", tc.count, len(plan))
		}
		nb := 0
		for _, h := range plan {
			if h.Broken {
				nb++
			}
		}
		if nb != tc.broken {
			t.Errorf("count=%d: got %d broken, want %d", tc.count, nb, tc.broken)
		}
	}
}

// TestEveryHostnameIsSynthetic is the safety invariant: every generated
// hostname carries the reserved suffix, every host maps to a real
// configcrate, and hostnames are unique.
func TestEveryHostnameIsSynthetic(t *testing.T) {
	plan := buildHostPlan(13647, 7)
	seen := make(map[string]bool, len(plan))
	suffix := "." + synthTLD
	for _, h := range plan {
		if !strings.HasSuffix(h.Hostname, suffix) {
			t.Fatalf("hostname %q lacks %q suffix", h.Hostname, suffix)
		}
		if h.Configcrate == "" {
			t.Fatalf("host %q has no configcrate", h.Hostname)
		}
		if _, ok := configcrateFiles[h.Configcrate+".vgo"]; !ok {
			t.Fatalf("host %q references unknown configcrate %q", h.Hostname, h.Configcrate)
		}
		if seen[h.Hostname] {
			t.Fatalf("duplicate hostname %q", h.Hostname)
		}
		seen[h.Hostname] = true
	}
}

// TestEveryMatchPatternIsSyntheticScoped is the other half of the safety
// invariant: no generated hostcrate pattern can match a real host,
// because every one ends in the reserved suffix.
func TestEveryMatchPatternIsSyntheticScoped(t *testing.T) {
	want := "." + synthTLD + `"`
	for _, r := range synthRegions {
		for _, line := range strings.Split(renderHostcrate(r), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "- match:") {
				continue
			}
			if !strings.HasSuffix(line, want) {
				t.Errorf("%s/%s: match pattern not synthetic-scoped: %q", r.cloud, r.name, line)
			}
		}
	}
}
