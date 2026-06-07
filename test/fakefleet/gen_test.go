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

	cleanup, err := writeConfigTree(stacks, "alexander4", buildHostPlan(400, 5, 3, 20))
	if err != nil {
		t.Fatal(err)
	}

	// Nested hostcrate exists; no top-level synthetic dir.
	if _, err := os.Stat(filepath.Join(stacks, "alexander4", "aws", "us-east-1", "us-east-1a", "envoys.vgo")); err != nil {
		t.Errorf("expected nested zone hostcrate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stacks, svcSubdir)); !os.IsNotExist(err) {
		t.Errorf("a top-level %s/ dir must not be created", svcSubdir)
	}
	// The operator's real file is untouched.
	if _, err := os.Stat(realFile); err != nil {
		t.Errorf("real fleet-dir file was disturbed: %v", err)
	}
	// Cleanup dirs are exactly the harness-owned ones, never the fleet dir.
	want := map[string]bool{
		filepath.Join(stacks, "configcrates", svcSubdir): true,
		filepath.Join(stacks, "alexander4", "aws"):       true,
		filepath.Join(stacks, "alexander4", "gcp"):       true,
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
	for _, h := range buildHostPlan(13647, 100, 20, 500) {
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

// TestHostPlanOutcomes verifies the plan lands the exact requested count
// and exactly the requested failed/changed split (rest settled), for both
// the default fleet size and arbitrary ones (distribution must always sum).
func TestHostPlanOutcomes(t *testing.T) {
	for _, tc := range []struct{ count, named, failed, changed int }{
		{13647, 100, 20, 500},
		{1000, 50, 5, 30},
		{100, 0, 0, 0},
	} {
		plan := buildHostPlan(tc.count, tc.named, tc.failed, tc.changed)
		if len(plan) != tc.count {
			t.Errorf("count=%d: got %d hosts", tc.count, len(plan))
		}
		got := map[string]int{}
		for _, h := range plan {
			got[h.Outcome]++
		}
		if got["failed"] != tc.failed || got["changed"] != tc.changed {
			t.Errorf("count=%d: outcomes %v, want failed=%d changed=%d", tc.count, got, tc.failed, tc.changed)
		}
		if got["ok"] != tc.count-tc.failed-tc.changed {
			t.Errorf("count=%d: ok=%d, want %d", tc.count, got["ok"], tc.count-tc.failed-tc.changed)
		}
	}
}

// TestEveryHostnameLooksSynthetic is the safety invariant: every generated
// hostname is bare (no domain), is positively identified by looksSynthetic
// (so backout's fallback finds it), maps to a real configcrate, and is
// unique. It also confirms the named/worker split lands the requested
// count of realistic service hostnames.
func TestEveryHostnameLooksSynthetic(t *testing.T) {
	const named = 100
	plan := buildHostPlan(13647, named, 20, 500)
	seen := make(map[string]bool, len(plan))
	gotNamed := 0
	for _, h := range plan {
		if strings.Contains(h.Hostname, ".") {
			t.Fatalf("hostname %q is not bare (carries a domain)", h.Hostname)
		}
		if !looksSynthetic(h.Hostname) {
			t.Fatalf("hostname %q is not identified by looksSynthetic", h.Hostname)
		}
		if !strings.HasPrefix(h.Hostname, "worker-") {
			gotNamed++
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
	if gotNamed != named {
		t.Errorf("got %d named (non-worker) hosts, want %d", gotNamed, named)
	}
}

// TestEveryMatchPatternIsZoneScoped is the other half of the safety
// invariant: every generated hostcrate pattern is anchored to a single
// zone (contains that zone's code) and ends in a trailing wildcard, so
// first-match-wins stays unambiguous and no pattern can match a host in
// another zone — or, post-rename, a real host (verified by TestLooksSynthetic).
func TestEveryMatchPatternIsZoneScoped(t *testing.T) {
	for _, r := range synthRegions {
		for _, az := range r.azs {
			zc := azCode(r, az)
			for _, line := range strings.Split(renderZoneHostcrate(r, az), "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "- match:") {
					continue
				}
				if !strings.HasSuffix(line, `-*"`) {
					t.Errorf("%s/%s/%s: pattern lacks trailing wildcard: %q", r.cloud, r.name, az, line)
				}
				if !strings.Contains(line, "-"+zc+"-") {
					t.Errorf("%s/%s/%s: pattern not scoped to zone %q: %q", r.cloud, r.name, az, zc, line)
				}
				if strings.Contains(line, "synthetic") {
					t.Errorf("%s/%s/%s: pattern still carries the synthetic marker: %q", r.cloud, r.name, az, line)
				}
			}
		}
	}
}

// TestLooksSynthetic confirms the backout fallback's positive identifier:
// generated names match, the operator's real hosts never do (even when
// bare), and arbitrary non-generated names don't.
func TestLooksSynthetic(t *testing.T) {
	synthetic := []string{
		"web-prod-use1a-007",
		"postgres-stage-usw2b-003",
		"worker-redis-stage-usw2b-01337",
		"worker-nginx-test-euw4a-00001",
	}
	for _, h := range synthetic {
		if !looksSynthetic(h) {
			t.Errorf("looksSynthetic(%q) = false, want true", h)
		}
	}
	notSynthetic := []string{
		"danlap", "annlap", "girlslaptop", "plex", "raspberrypi",
		"annlap.lan", "PLEX.local", // real hosts, with/without domain + case
		"vigosrv-host", "localhost", "db-server-01",
		"web-prod-use1a", "worker-redis-stage-usw2b", // too few parts
		"web-prod-use1a-x07",        // non-numeric tail
		"web-prod-zzz9z-007",        // unknown zone
		"frobnicate-prod-use1a-007", // unknown class
	}
	for _, h := range notSynthetic {
		if looksSynthetic(h) {
			t.Errorf("looksSynthetic(%q) = true, want false", h)
		}
	}
}
