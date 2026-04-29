// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Pair-mapping helpers ---

func TestRetractRelFromExampleRel(t *testing.T) {
	cases := map[string]string{
		"modules/databases/postgres.vgo.example": "modules/databases/postgres-retract.vgo.example",
		"modules/web/nginx.vgo.example":          "modules/web/nginx-retract.vgo.example",
		"modules/foo.vgo.example":                "modules/foo-retract.vgo.example",
	}
	for in, want := range cases {
		if got := retractRelFromExampleRel(in); got != want {
			t.Errorf("retractRelFromExampleRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExampleRelFromRetractRel(t *testing.T) {
	cases := map[string]string{
		"modules/databases/postgres-retract.vgo.example": "modules/databases/postgres.vgo.example",
		"modules/web/nginx-retract.vgo.example":          "modules/web/nginx.vgo.example",
		"modules/foo-retract.vgo.example":                "modules/foo.vgo.example",
	}
	for in, want := range cases {
		if got := exampleRelFromRetractRel(in); got != want {
			t.Errorf("exampleRelFromRetractRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPairMappingRoundTrip(t *testing.T) {
	// Round-trip: example → retract → example must be the identity.
	for _, ex := range []string{
		"modules/databases/postgres.vgo.example",
		"modules/networking/router-base.vgo.example",
		"modules/foo.vgo.example",
	} {
		round := exampleRelFromRetractRel(retractRelFromExampleRel(ex))
		if round != ex {
			t.Errorf("round-trip drift: %q → %q", ex, round)
		}
	}
}

// --- Walker helpers ---

func TestCollectModuleExamples_SkipsRetractsAndNonExamples(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "modules", "web", "nginx.vgo.example"), "")
	mustWrite(t, filepath.Join(dir, "modules", "databases", "postgres.vgo.example"), "")
	// A leaked retract under examples/ — must NOT count as an example.
	mustWrite(t, filepath.Join(dir, "modules", "web", "nginx-retract.vgo.example"), "")
	// Non-example file — must be ignored.
	mustWrite(t, filepath.Join(dir, "modules", "web", "README.md"), "x")
	// Outside modules/ — must be ignored.
	mustWrite(t, filepath.Join(dir, "usercrates", "alice.vgo.example"), "")

	got := collectModuleExamples(dir)
	want := map[string]struct{}{
		"modules/web/nginx.vgo.example":          {},
		"modules/databases/postgres.vgo.example": {},
	}
	if !mapsEqual(got, want) {
		t.Errorf("collectModuleExamples = %v, want %v", got, want)
	}
}

func TestCollectModuleAntiExamples_OnlyRetractFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "modules", "web", "nginx-retract.vgo.example"), "")
	mustWrite(t, filepath.Join(dir, "modules", "databases", "postgres-retract.vgo.example"), "")
	// A non-retract example leaked into anti-examples — must NOT count.
	mustWrite(t, filepath.Join(dir, "modules", "web", "stray.vgo.example"), "")
	// Non-example file.
	mustWrite(t, filepath.Join(dir, "modules", "web", "notes.txt"), "")

	got := collectModuleAntiExamples(dir)
	want := map[string]struct{}{
		"modules/web/nginx-retract.vgo.example":          {},
		"modules/databases/postgres-retract.vgo.example": {},
	}
	if !mapsEqual(got, want) {
		t.Errorf("collectModuleAntiExamples = %v, want %v", got, want)
	}
}

func TestCollectModuleExamples_MissingDirReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	// No modules/ subdir at all.
	if got := collectModuleExamples(dir); len(got) != 0 {
		t.Errorf("expected empty map for missing modules dir, got %v", got)
	}
	if got := collectModuleAntiExamples(dir); len(got) != 0 {
		t.Errorf("expected empty map for missing modules dir, got %v", got)
	}
}

// --- runMirrorCheck integration ---

func TestRunMirrorCheck_PairedTreePasses(t *testing.T) {
	exDir, antiDir := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(exDir, "modules", "web", "nginx.vgo.example"), "")
	mustWrite(t, filepath.Join(antiDir, "modules", "web", "nginx-retract.vgo.example"), "")

	if err := runMirrorCheck(exDir, antiDir); err != nil {
		t.Errorf("expected nil for fully-paired tree, got: %v", err)
	}
}

func TestRunMirrorCheck_MissingRetractFails(t *testing.T) {
	exDir, antiDir := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(exDir, "modules", "web", "nginx.vgo.example"), "")
	mustWrite(t, filepath.Join(exDir, "modules", "databases", "mysql.vgo.example"), "")
	mustWrite(t, filepath.Join(antiDir, "modules", "web", "nginx-retract.vgo.example"), "")
	// mysql has no matching retract.

	err := runMirrorCheck(exDir, antiDir)
	if err == nil {
		t.Fatal("expected error for missing retract, got nil")
	}
	if !strings.Contains(err.Error(), "1 missing") {
		t.Errorf("error should report 1 missing; got: %v", err)
	}
}

func TestRunMirrorCheck_OrphanRetractFails(t *testing.T) {
	exDir, antiDir := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(exDir, "modules", "web", "nginx.vgo.example"), "")
	mustWrite(t, filepath.Join(antiDir, "modules", "web", "nginx-retract.vgo.example"), "")
	mustWrite(t, filepath.Join(antiDir, "modules", "deprecated", "old-retract.vgo.example"), "")
	// deprecated/old has a retract but no source example.

	err := runMirrorCheck(exDir, antiDir)
	if err == nil {
		t.Fatal("expected error for orphan retract, got nil")
	}
	if !strings.Contains(err.Error(), "1 orphan") {
		t.Errorf("error should report 1 orphan; got: %v", err)
	}
}

func TestRunMirrorCheck_BothMissingAndOrphan(t *testing.T) {
	exDir, antiDir := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(exDir, "modules", "web", "nginx.vgo.example"), "")          // missing retract
	mustWrite(t, filepath.Join(exDir, "modules", "databases", "mysql.vgo.example"), "")    // missing retract
	mustWrite(t, filepath.Join(antiDir, "modules", "deprecated", "old-retract.vgo.example"), "") // orphan

	err := runMirrorCheck(exDir, antiDir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "2 missing") || !strings.Contains(err.Error(), "1 orphan") {
		t.Errorf("error should report 2 missing + 1 orphan; got: %v", err)
	}
}

func TestRunMirrorCheck_EmptyTreesPass(t *testing.T) {
	exDir, antiDir := t.TempDir(), t.TempDir()
	if err := runMirrorCheck(exDir, antiDir); err != nil {
		t.Errorf("empty trees should pass; got: %v", err)
	}
}

// --- findExamples multi-tree walking ---

func TestFindExamples_WalksMultipleTrees(t *testing.T) {
	// `vigocli config examples list` walks both stacks/examples/ and
	// stacks/anti-examples/ so install templates and their paired
	// retracts surface from one command.
	exDir := t.TempDir()
	antiDir := t.TempDir()
	mustWrite(t, filepath.Join(exDir, "modules", "web", "nginx.vgo.example"), "")
	mustWrite(t, filepath.Join(exDir, "modules", "databases", "postgres.vgo.example"), "")
	mustWrite(t, filepath.Join(antiDir, "modules", "web", "nginx-retract.vgo.example"), "")
	mustWrite(t, filepath.Join(antiDir, "modules", "databases", "postgres-retract.vgo.example"), "")

	got, err := findExamples(exDir, antiDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("findExamples merged count = %d, want 4", len(got))
	}
	// Each tree contributes paths relative to its own root, so the
	// rel paths line up cleanly when used to compute stockpile dests.
	wantRels := map[string]struct{}{
		"modules/databases/postgres.vgo.example":         {},
		"modules/databases/postgres-retract.vgo.example": {},
		"modules/web/nginx.vgo.example":                  {},
		"modules/web/nginx-retract.vgo.example":          {},
	}
	for _, ex := range got {
		if _, ok := wantRels[ex.rel]; !ok {
			t.Errorf("unexpected rel %q in merged list", ex.rel)
		}
	}
}

func TestFindExamples_SkipsMissingDirsButRequiresAtLeastOne(t *testing.T) {
	exDir := t.TempDir()
	mustWrite(t, filepath.Join(exDir, "modules", "web", "nginx.vgo.example"), "")
	// One real dir, one nonexistent — should succeed with the real one.
	got, err := findExamples(exDir, filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Errorf("missing tree should be skipped silently: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d, want 1", len(got))
	}
	// Both nonexistent — must error.
	if _, err := findExamples(filepath.Join(t.TempDir(), "x"), filepath.Join(t.TempDir(), "y")); err == nil {
		t.Errorf("all-missing should error")
	}
}

// --- helpers ---

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mapsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}
