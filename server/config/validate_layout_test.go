// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Single-tree validator cases (post-0.33.0 collapse). The legacy
// stacks/scaffolding split is gone — every claim and waiver lives
// under the single stacks/ tree.
//
//   1. compliance.vgo carrying a top-level waivers: block — mixed-keys.
//   2. waivers.vgo carrying a top-level compliance: block — mixed-keys.
//   3. Stray-filename: top-level waivers: in a non-waivers.vgo file.
//   4. Stray-filename: claims-only file not named compliance.vgo.
//
// Module definitions (with name:/resources:) carrying their own
// compliance: block must NOT trip the claims-only check.

func TestValidate_WaiversKeyInComplianceVgo_Rejected(t *testing.T) {
	dir := t.TempDir()
	stacksDir := filepath.Join(dir, "stacks")
	_ = os.MkdirAll(stacksDir, 0o755)
	_ = os.WriteFile(filepath.Join(stacksDir, "compliance.vgo"), []byte(
		"compliance:\n  cis-ubuntu: [\"1.1.1\"]\n"+
			"waivers:\n  cis-ubuntu:\n    - control: \"5.4.1\"\n      reason: test\n      approved_by: dan\n"), 0o644)

	err := validateStacksLayout(stacksDir)
	if err == nil {
		t.Fatal("expected violation for waivers: block in compliance.vgo, got nil")
	}
	if !strings.Contains(err.Error(), "waivers: block in compliance.vgo") {
		t.Errorf("error should call out the wrong-key-in-right-file case; got: %s", err)
	}
	if !strings.Contains(err.Error(), "Move the waivers: entries to waivers.vgo") {
		t.Errorf("error should point at waivers.vgo; got: %s", err)
	}
}

func TestValidate_ComplianceKeyInWaiversVgo_Rejected(t *testing.T) {
	dir := t.TempDir()
	stacksDir := filepath.Join(dir, "stacks")
	_ = os.MkdirAll(stacksDir, 0o755)
	_ = os.WriteFile(filepath.Join(stacksDir, "waivers.vgo"), []byte(
		"waivers:\n  cis-ubuntu:\n    - control: \"5.4.1\"\n      reason: test\n      approved_by: dan\n"+
			"compliance:\n  cis-ubuntu: [\"1.1.1\"]\n"), 0o644)

	err := validateStacksLayout(stacksDir)
	if err == nil {
		t.Fatal("expected violation for compliance: block in waivers.vgo, got nil")
	}
	if !strings.Contains(err.Error(), "compliance: block in waivers.vgo") {
		t.Errorf("error should call out the wrong-key-in-right-file case; got: %s", err)
	}
	if !strings.Contains(err.Error(), "Move the compliance: claims to compliance.vgo") {
		t.Errorf("error should point at compliance.vgo; got: %s", err)
	}
}

func TestValidate_StrayWaiversFilename_Rejected(t *testing.T) {
	dir := t.TempDir()
	stacksDir := filepath.Join(dir, "stacks")
	_ = os.MkdirAll(stacksDir, 0o755)
	_ = os.WriteFile(filepath.Join(stacksDir, "corp-waivers.vgo"),
		[]byte("waivers:\n  cis-ubuntu:\n    - control: \"5.4.1\"\n      reason: r\n      approved_by: d\n"), 0o644)

	err := validateStacksLayout(stacksDir)
	if err == nil {
		t.Fatal("expected violation for waivers: block in non-waivers.vgo file, got nil")
	}
	if !strings.Contains(err.Error(), "Rename to waivers.vgo") {
		t.Errorf("error should include rename hint; got: %s", err)
	}
}

func TestValidate_StrayClaimsFilename_Rejected(t *testing.T) {
	dir := t.TempDir()
	stacksDir := filepath.Join(dir, "stacks")
	_ = os.MkdirAll(stacksDir, 0o755)
	_ = os.WriteFile(filepath.Join(stacksDir, "corp-compliance.vgo"),
		[]byte("compliance:\n  cis-ubuntu: [\"5.1.1\"]\n"), 0o644)

	err := validateStacksLayout(stacksDir)
	if err == nil {
		t.Fatal("expected violation for claims-only file not named compliance.vgo, got nil")
	}
	if !strings.Contains(err.Error(), "Rename to compliance.vgo") {
		t.Errorf("error should include rename hint; got: %s", err)
	}
}

// Module files (with name:/resources:) carrying a top-level compliance:
// block are NOT claims-only — they're module defs declaring their own
// claims, and must NOT be rejected.
func TestValidate_ConfigcrateWithComplianceBlock_NotRejected(t *testing.T) {
	dir := t.TempDir()
	stacksDir := filepath.Join(dir, "configcrates")
	_ = os.MkdirAll(stacksDir, 0o755)
	_ = os.WriteFile(filepath.Join(stacksDir, "openssh.vgo"),
		[]byte("name: openssh\ncompliance:\n  cis-ubuntu: [\"5.4.1\"]\nresources:\n  - name: foo\n    type: package\n    package: openssh-server\n"), 0o644)

	if err := validateStacksLayout(stacksDir); err != nil {
		t.Errorf("module file with compliance: block should pass; got: %s", err)
	}
}

func TestValidate_CleanLayout_Passes(t *testing.T) {
	dir := t.TempDir()
	stacksDir := filepath.Join(dir, "stacks")
	_ = os.MkdirAll(filepath.Join(stacksDir, "configcrates"), 0o755)
	_ = os.WriteFile(filepath.Join(stacksDir, "compliance.vgo"),
		[]byte("compliance:\n  cis-ubuntu: [\"1.1.1\"]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(stacksDir, "waivers.vgo"),
		[]byte("waivers:\n  cis-ubuntu:\n    - control: \"5.4.1\"\n      reason: r\n      approved_by: d\n"), 0o644)

	if err := validateStacksLayout(stacksDir); err != nil {
		t.Errorf("clean stacks should pass validation, got: %s", err)
	}
}

func TestValidate_AllFourViolations_ReportedTogether(t *testing.T) {
	dir := t.TempDir()
	stacksDir := filepath.Join(dir, "stacks")
	_ = os.MkdirAll(stacksDir, 0o755)

	// Mixed keys in compliance.vgo
	_ = os.WriteFile(filepath.Join(stacksDir, "compliance.vgo"), []byte(
		"compliance:\n  cis-ubuntu: [\"1\"]\n"+
			"waivers:\n  cis-ubuntu:\n    - control: \"x\"\n      reason: r\n      approved_by: d\n"), 0o644)
	// Mixed keys in waivers.vgo
	_ = os.WriteFile(filepath.Join(stacksDir, "waivers.vgo"), []byte(
		"waivers:\n  cis-ubuntu:\n    - control: \"x\"\n      reason: r\n      approved_by: d\n"+
			"compliance:\n  cis-ubuntu: [\"bad\"]\n"), 0o644)
	// Stray waivers filename
	_ = os.WriteFile(filepath.Join(stacksDir, "corp-waivers.vgo"),
		[]byte("waivers:\n  cis-ubuntu:\n    - control: \"x\"\n      reason: r\n      approved_by: d\n"), 0o644)
	// Stray claims filename
	_ = os.WriteFile(filepath.Join(stacksDir, "corp-compliance.vgo"),
		[]byte("compliance:\n  cis-ubuntu: [\"5.1.1\"]\n"), 0o644)

	err := validateStacksLayout(stacksDir)
	if err == nil {
		t.Fatal("expected violations, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"waivers: block in compliance.vgo",
		"compliance: block in waivers.vgo",
		"Rename to waivers.vgo",
		"Rename to compliance.vgo",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("combined error missing %q; got:\n%s", want, msg)
		}
	}
}
