// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/modlint"
)

// TestWriteConfigTreeNestsUnderFleetDir locks the layout + backout-safety
// invariant: hostcrates nest under <fleetDir>/<cloud>/..., no top-level
// `synthetic/` dir is created, the operator's existing fleet-dir config is
// untouched, and the reported cleanup dirs are exactly the harness-owned
// ones (never the fleet dir itself).
func TestWriteConfigTreeNestsUnderFleetDir(t *testing.T) {
	stacks := t.TempDir()
	// A pre-existing "real" file in the fleet dir that backout must never reach.
	real := filepath.Join(stacks, "alexander4", "us-east1", "us-east1-a")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(real, "girlslaptop.vgo")
	if err := os.WriteFile(realFile, []byte("envoys: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanup, err := writeConfigTree(stacks, "alexander4", buildHostPlan(400, 3))
	if err != nil {
		t.Fatal(err)
	}

	// Nested hostcrate exists; no top-level synthetic dir.
	if _, err := os.Stat(filepath.Join(stacks, "alexander4", "aws", "us-east-1", "us-east-1a", "envoys.vgo")); err != nil {
		t.Errorf("expected nested zone hostcrate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stacks, synthTLD)); !os.IsNotExist(err) {
		t.Errorf("a top-level %s/ dir must not be created", synthTLD)
	}
	// The operator's real file is untouched.
	if _, err := os.Stat(realFile); err != nil {
		t.Errorf("real fleet-dir file was disturbed: %v", err)
	}
	// Cleanup dirs are exactly the harness-owned ones, never the fleet dir.
	want := map[string]bool{
		filepath.Join(stacks, "configcrates", synthTLD): true,
		filepath.Join(stacks, "alexander4", "aws"):      true,
		filepath.Join(stacks, "alexander4", "gcp"):      true,
	}
	if len(cleanup) != len(want) {
		t.Fatalf("cleanup dirs = %v, want the 3 harness-owned dirs", cleanup)
	}
	for _, d := range cleanup {
		if !want[d] {
			t.Errorf("unexpected cleanup dir %q", d)
		}
		if d == filepath.Join(stacks, "alexander4") {
			t.Fatalf("cleanup must never include the fleet dir itself")
		}
	}
}

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

// TestHostcratesLintClean asserts every generated per-zone hostcrate
// passes the hostcrate linter clean.
func TestHostcratesLintClean(t *testing.T) {
	for _, r := range synthRegions {
		for _, az := range r.azs {
			body := renderZoneHostcrate(r, az)
			res := modlint.LintHostcrate([]byte(body), modlint.Options{})
			if !res.OK() {
				t.Errorf("hostcrate %s/%s/%s has lint errors: %v", r.cloud, r.name, az, res.Errors)
			}
		}
	}
}

// TestEnvironmentsFileLintClean asserts the synthetic environments.vgo
// passes the environments linter clean — it's published like any other
// scaffolding file.
func TestEnvironmentsFileLintClean(t *testing.T) {
	res := modlint.LintEnvironmentsFile([]byte(environmentsFile), modlint.Options{})
	if !res.OK() {
		t.Errorf("environments.vgo has lint errors: %v", res.Errors)
	}
}

// TestEnvAxisCovered confirms every host carries one of the three
// environments and all three are represented — the multi-axis env
// dimension is populated, not collapsed.
func TestEnvAxisCovered(t *testing.T) {
	valid := map[string]bool{"prod": true, "stage": true, "test": true}
	counts := map[string]int{}
	for _, h := range buildHostPlan(13647, 7) {
		if !valid[h.Env] {
			t.Fatalf("host %q has invalid env %q", h.Hostname, h.Env)
		}
		counts[h.Env]++
	}
	for env := range valid {
		if counts[env] == 0 {
			t.Errorf("env %q has no hosts", env)
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
		for _, az := range r.azs {
			for _, line := range strings.Split(renderZoneHostcrate(r, az), "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "- match:") {
					continue
				}
				if !strings.HasSuffix(line, want) {
					t.Errorf("%s/%s/%s: match pattern not synthetic-scoped: %q", r.cloud, r.name, az, line)
				}
			}
		}
	}
}
