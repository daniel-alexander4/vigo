// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestComplianceSeedGate pins the license boundary that keeps the paid
// compliance configcrates out of the free stacks/examples/ and
// stacks/anti-examples/ reference trees.
//
// entrypoint.sh rebuilds those two trees from /seed on every container
// start and excludes the compliance content with two `case ... continue`
// skip-globs. Those globs are bare path literals, so a directory rename
// silently un-gates the content if the globs aren't updated in lockstep:
// the modules→configcrates rename (0.71.0) did exactly that, leaking
// ~100 compliance configcrates + ~100 retractions into the free trees on
// every start (the globs still said modules/compliance/). This test fails
// the next such drift in CI instead of in a customer's free install.
//
// The guard ties three facts together:
//   - entrypoint.sh carries the exact skip-glob for each tree (so a glob
//     edit without a matching seed-dir move fails here), AND
//   - the compliance seed dirs are non-empty (so the test can't pass
//     vacuously on an empty tree — half of the original failure was that
//     the glob matched nothing), AND
//   - every compliance seed file's seed-relative path is actually covered
//     by that glob's prefix (so a seed-dir move without a glob edit fails
//     here too).
func TestComplianceSeedGate(t *testing.T) {
	root := repoRoot(t)

	entrypoint, err := os.ReadFile(filepath.Join(root, "entrypoint.sh"))
	if err != nil {
		t.Fatalf("read entrypoint.sh: %v", err)
	}
	ep := string(entrypoint)

	// Each tree: the seed source dir holding the compliance configcrates,
	// the seed-relative prefix entrypoint.sh strips to, and the skip-glob
	// that must appear verbatim in entrypoint.sh. The glob is the prefix
	// plus a trailing `*` (shell case `*` spans `/`).
	cases := []struct {
		name      string // human label
		seedDir   string // repo-relative dir to walk for *.vgo.example
		relPrefix string // seed-relative path prefix the skip-glob covers
	}{
		{
			name:      "examples",
			seedDir:   "example-configs/worksite/stockpile/configcrates/compliance",
			relPrefix: "stockpile/configcrates/compliance/",
		},
		{
			name:      "anti-examples (retractions)",
			seedDir:   "example-configs/retractions/configcrates/compliance",
			relPrefix: "configcrates/compliance/",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			glob := tc.relPrefix + "*"
			if !strings.Contains(ep, glob) {
				t.Errorf("entrypoint.sh is missing the compliance skip-glob %q — the %s tree no longer gates the paid compliance configcrates (license leak). If the seed layout was renamed, update the skip `case` in entrypoint.sh to match.", glob, tc.name)
			}

			dir := filepath.Join(root, tc.seedDir)
			var count int
			err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() || !strings.HasSuffix(path, ".vgo.example") {
					return nil
				}
				count++
				// The seed-relative path entrypoint.sh computes for this
				// file must fall under the skip-glob's prefix, or it leaks.
				rel := tc.relPrefix + strings.TrimPrefix(path, dir+string(os.PathSeparator))
				if !strings.HasPrefix(rel, tc.relPrefix) {
					t.Errorf("%s: seed-relative path %q is not covered by skip-glob prefix %q", tc.name, rel, tc.relPrefix)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("%s: walk %s: %v", tc.name, tc.seedDir, err)
			}
			if count == 0 {
				t.Errorf("%s: found 0 compliance *.vgo.example under %s — the guard would pass vacuously. If compliance seed content moved, point this test at its new home.", tc.name, tc.seedDir)
			}
		})
	}
}
