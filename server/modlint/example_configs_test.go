// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestExampleModulesLintClean walks every shipped .vgo.example under
// example-configs/ and asserts modlint produces no Error-severity
// findings. Gates the schema/executor/example triangle so the bug class
// that surfaced ~30 lint failures during the 2026-05-17 GCP install
// dry-run can't ship again silently.
//
// Roots walked:
//   - example-configs/worksite/stockpile  (modules + usercrates + common.vgo + hostcrates)
//   - example-configs/retractions         (paired retract templates)
//   - example-configs/worksite/scaffolding (roles.vgo + scaffolding common.vgo)
//
// Dispatch matches server/publish/publish.go: scaffolding-file basenames
// route to LintHostcrate / LintRolesFile / LintWaiversFile /
// LintEnvironmentsFile; compliance.vgo files are skipped (loaded by the
// config layer, not modlint); usercrate files set IsUsercrate=true so
// the filename-stem fallback for `name:` matches the loader.
//
// Warnings are tolerated; Errors are not.
func TestExampleModulesLintClean(t *testing.T) {
	root := exampleConfigsRoot(t)
	walked := 0
	failures := map[string][]string{}

	subdirs := []string{
		"worksite/stockpile",
		"retractions",
		"worksite/scaffolding",
	}
	for _, sub := range subdirs {
		base := filepath.Join(root, sub)
		if _, err := os.Stat(base); err != nil {
			t.Fatalf("example-configs root missing %s: %v", base, err)
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".vgo.example") {
				return nil
			}
			// Effective basename: strip .example so the dispatch table
			// can match the same names publish.go uses.
			effective := strings.TrimSuffix(filepath.Base(path), ".example")
			// compliance.vgo + common.vgo are processed by the config
			// loader, not modlint. publish.go's lintModules() filters
			// them out before dispatch (compliance.vgo via an explicit
			// continue; common.vgo because filterBySubdir+filterUser-
			// crates+filterScaffoldingFiles never picks it up). Mirror
			// that here so the test asks the same question the publish
			// path asks.
			if effective == "compliance.vgo" || effective == "common.vgo" {
				return nil
			}
			walked++
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			opts := Options{
				IsUsercrate: strings.Contains(filepath.ToSlash(path), "/usercrates/"),
			}
			var res *Result
			switch effective {
			case "envoys.vgo":
				res = LintHostcrate(data, opts)
			case "roles.vgo":
				res = LintRolesFile(data, opts)
			case "waivers.vgo":
				res = LintWaiversFile(data, opts)
			case "environments.vgo":
				res = LintEnvironmentsFile(data, opts)
			default:
				res = Lint(data, opts)
			}
			if !res.OK() {
				rel, _ := filepath.Rel(root, path)
				failures[rel] = append([]string{}, res.Errors...)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}

	if walked == 0 {
		t.Fatalf("found zero .vgo.example files under %s — wrong root?", root)
	}
	if len(failures) == 0 {
		t.Logf("linted %d .vgo.example files cleanly", walked)
		return
	}

	paths := make([]string, 0, len(failures))
	for p := range failures {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "  %s\n", p)
		for _, msg := range failures[p] {
			fmt.Fprintf(&b, "    - %s\n", msg)
		}
	}
	t.Fatalf("modlint Error findings across %d/%d example-configs files:\n%sTotal files walked: %d, files with errors: %d",
		len(failures), walked, b.String(), walked, len(failures))
}

// exampleConfigsRoot walks up from the test's working dir to find the
// example-configs/ tree. Tests run with pwd=server/modlint/, so the
// repo root is two levels up — but resolve dynamically so the test
// keeps working if the package ever moves.
func exampleConfigsRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "example-configs")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("example-configs/ not found walking up from %s", dir)
		}
		dir = parent
	}
}
