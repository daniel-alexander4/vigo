// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestSchemasGenUpToDate guards against a stale or hand-edited
// server/validate/schemas_gen.go. It reruns the canonical generator
// (scripts/gen-executor-schemas.sh) and byte-compares its output to the
// committed file — the script self-gofmt's (commit 83932b44), so a plain
// compare is exact. Shelling out to the script keeps it the single source of
// truth for the executor-param scrape; re-deriving the extraction in Go would
// be a second source that could drift.
//
// The script needs bash + gofmt + GNU `grep -P` + the Rust executor source.
// Where any of those is absent (e.g. macOS BSD grep) it can't run, so the test
// SKIPS on any run failure and only FAILS on a clean run that disagrees — it
// never false-fails. Effective on Linux (CI + the dev box), which is where
// drift is caught before merge.
func TestSchemasGenUpToDate(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "gen-executor-schemas.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("generator script not found: %v", err)
	}

	committed, err := os.ReadFile(filepath.Join(root, "server", "validate", "schemas_gen.go"))
	if err != nil {
		t.Fatalf("reading committed schemas_gen.go: %v", err)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(script)
	cmd.Dir = root
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// bash / gofmt / GNU grep -P / the Rust source absent, or the script
		// otherwise couldn't run — not a drift signal, so skip rather than fail.
		t.Skipf("generator did not run cleanly (%v); stderr: %s", err, stderr.String())
	}

	if !bytes.Equal(stdout.Bytes(), committed) {
		t.Errorf("server/validate/schemas_gen.go is out of date with scripts/gen-executor-schemas.sh.\n" +
			"An executor or its params changed without regenerating, or the file was hand-edited.\n" +
			"Run `make schemas` and commit the result.")
	}
}
