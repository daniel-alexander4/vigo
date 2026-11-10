// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// Where the toolchain is absent (e.g. macOS BSD grep) the test SKIPS, naming
// what is missing. With the toolchain present, a generator that fails is a
// FAILURE: a generator regression must not hide the very drift it guards.
func TestSchemasGenUpToDate(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "gen-executor-schemas.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("generator script missing (it is tracked): %v", err)
	}
	if missing := missingSchemaGenTools(); missing != "" {
		t.Skipf("generator toolchain unavailable: %s", missing)
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
		t.Fatalf("generator failed with its toolchain present (%v) — fix scripts/gen-executor-schemas.sh; stderr: %s", err, stderr.String())
	}

	if !bytes.Equal(stdout.Bytes(), committed) {
		t.Errorf("server/validate/schemas_gen.go is out of date with scripts/gen-executor-schemas.sh.\n" +
			"An executor or its params changed without regenerating, or the file was hand-edited.\n" +
			"Run `make schemas` and commit the result.")
	}
}

// missingSchemaGenTools names the first generator prerequisite that is absent:
// bash, gofmt, or a grep that supports -P (GNU grep; BSD grep does not).
// Empty when all are present.
func missingSchemaGenTools() string {
	for _, tool := range []string{"bash", "gofmt", "grep"} {
		if _, err := exec.LookPath(tool); err != nil {
			return tool + " not on PATH"
		}
	}
	// grep -P exits 0/1 on match/no-match and 2 when -P is unsupported.
	probe := exec.Command("grep", "-P", "x")
	probe.Stdin = strings.NewReader("x\n")
	if err := probe.Run(); err != nil {
		return "grep has no -P (PCRE) support"
	}
	return ""
}
