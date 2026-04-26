// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Four filename-per-tree validator cases:
//   1. waivers.vgo in stockpile → reject
//   2. compliance.vgo in scaffolding → reject
//   3. waivers: key inside stockpile/compliance.vgo → reject
//   4. compliance: key inside scaffolding/waivers.vgo → reject
//
// Each fires fatally with a specific remediation hint. All four are
// collected in a single error so operators see every problem per pass.

func TestValidate_WaiversVgoInStockpile_Rejected(t *testing.T) {
	dir := t.TempDir()
	stockpileDir := filepath.Join(dir, "stockpile")
	_ = os.MkdirAll(stockpileDir, 0o755)
	_ = os.WriteFile(filepath.Join(stockpileDir, "waivers.vgo"), []byte("waivers:\n  cis:\n    - control: \"5.4.1\"\n"), 0o644)

	err := validateWorksiteLayout(dir)
	if err == nil {
		t.Fatal("expected layout violation for waivers.vgo in stockpile, got nil")
	}
	if !strings.Contains(err.Error(), "scaffolding") {
		t.Errorf("error should mention scaffolding as the correct tree; got: %s", err)
	}
	if !strings.Contains(err.Error(), "Move to scaffolding/waivers.vgo") {
		t.Errorf("error should include move-to-scaffolding hint; got: %s", err)
	}
}

func TestValidate_ComplianceVgoInScaffolding_Rejected(t *testing.T) {
	dir := t.TempDir()
	scaffoldingDir := filepath.Join(dir, "scaffolding")
	_ = os.MkdirAll(scaffoldingDir, 0o755)
	_ = os.WriteFile(filepath.Join(scaffoldingDir, "compliance.vgo"), []byte("compliance:\n  cis: [\"1.1.1\"]\n"), 0o644)

	err := validateWorksiteLayout(dir)
	if err == nil {
		t.Fatal("expected layout violation for compliance.vgo in scaffolding, got nil")
	}
	if !strings.Contains(err.Error(), "stockpile") {
		t.Errorf("error should mention stockpile as the correct tree; got: %s", err)
	}
	if !strings.Contains(err.Error(), "Move to stockpile/compliance.vgo") {
		t.Errorf("error should include move-to-stockpile hint; got: %s", err)
	}
}

func TestValidate_WaiversKeyInStockpileCompliance_Rejected(t *testing.T) {
	dir := t.TempDir()
	stockpileDir := filepath.Join(dir, "stockpile")
	_ = os.MkdirAll(stockpileDir, 0o755)
	_ = os.WriteFile(filepath.Join(stockpileDir, "compliance.vgo"), []byte(
		"compliance:\n  cis: [\"1.1.1\"]\n"+
			"waivers:\n  cis:\n    - control: \"5.4.1\"\n      reason: test\n      approved_by: dan\n"), 0o644)

	err := validateWorksiteLayout(dir)
	if err == nil {
		t.Fatal("expected violation for waivers: block in stockpile/compliance.vgo, got nil")
	}
	if !strings.Contains(err.Error(), "waivers: block in a stockpile compliance.vgo") {
		t.Errorf("error should call out the wrong-key-in-right-file case; got: %s", err)
	}
	if !strings.Contains(err.Error(), "scaffolding/waivers.vgo") {
		t.Errorf("error should point at scaffolding/waivers.vgo; got: %s", err)
	}
}

func TestValidate_ComplianceKeyInScaffoldingWaivers_Rejected(t *testing.T) {
	dir := t.TempDir()
	scaffoldingDir := filepath.Join(dir, "scaffolding")
	_ = os.MkdirAll(scaffoldingDir, 0o755)
	_ = os.WriteFile(filepath.Join(scaffoldingDir, "waivers.vgo"), []byte(
		"waivers:\n  cis:\n    - control: \"5.4.1\"\n      reason: test\n      approved_by: dan\n"+
			"compliance:\n  cis: [\"1.1.1\"]\n"), 0o644)

	err := validateWorksiteLayout(dir)
	if err == nil {
		t.Fatal("expected violation for compliance: block in scaffolding/waivers.vgo, got nil")
	}
	if !strings.Contains(err.Error(), "compliance: block in a scaffolding waivers.vgo") {
		t.Errorf("error should call out the wrong-key-in-right-file case; got: %s", err)
	}
	if !strings.Contains(err.Error(), "stockpile/compliance.vgo") {
		t.Errorf("error should point at stockpile/compliance.vgo; got: %s", err)
	}
}

// All four problems in one worksite — validator returns all of them in a
// single error so operators don't play whack-a-mole.
func TestValidate_AllFourViolations_ReportedTogether(t *testing.T) {
	dir := t.TempDir()
	stockpileDir := filepath.Join(dir, "stockpile")
	scaffoldingDir := filepath.Join(dir, "scaffolding")
	_ = os.MkdirAll(stockpileDir, 0o755)
	_ = os.MkdirAll(scaffoldingDir, 0o755)

	// Violation 1: waivers.vgo in stockpile
	_ = os.WriteFile(filepath.Join(stockpileDir, "waivers.vgo"), []byte("waivers:\n  cis:\n    - control: \"x\"\n"), 0o644)
	// Violation 2: compliance.vgo in scaffolding
	_ = os.WriteFile(filepath.Join(scaffoldingDir, "compliance.vgo"), []byte("compliance:\n  cis: [\"1\"]\n"), 0o644)
	// Violations 3+4 require separate files (otherwise filename-in-wrong-tree
	// fires first). Add a separate correct-name file with wrong key:
	_ = os.WriteFile(filepath.Join(stockpileDir, "compliance.vgo"), []byte(
		"compliance:\n  cis: [\"ok\"]\n"+
			"waivers:\n  cis:\n    - control: \"x\"\n      reason: r\n      approved_by: d\n"), 0o644)
	_ = os.WriteFile(filepath.Join(scaffoldingDir, "waivers.vgo"), []byte(
		"waivers:\n  cis:\n    - control: \"x\"\n      reason: r\n      approved_by: d\n"+
			"compliance:\n  cis: [\"bad\"]\n"), 0o644)

	err := validateWorksiteLayout(dir)
	if err == nil {
		t.Fatal("expected violations, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"stockpile/waivers.vgo",
		"scaffolding/compliance.vgo",
		"waivers: block in a stockpile compliance.vgo",
		"compliance: block in a scaffolding waivers.vgo",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("combined error missing %q; got:\n%s", want, msg)
		}
	}
}

func TestValidate_CleanLayout_Passes(t *testing.T) {
	dir := t.TempDir()
	stockpileDir := filepath.Join(dir, "stockpile", "modules")
	scaffoldingDir := filepath.Join(dir, "scaffolding")
	_ = os.MkdirAll(stockpileDir, 0o755)
	_ = os.MkdirAll(scaffoldingDir, 0o755)

	_ = os.WriteFile(filepath.Join(dir, "stockpile", "compliance.vgo"),
		[]byte("compliance:\n  cis: [\"1.1.1\"]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(scaffoldingDir, "waivers.vgo"),
		[]byte("waivers:\n  cis:\n    - control: \"5.4.1\"\n      reason: r\n      approved_by: d\n"), 0o644)

	if err := validateWorksiteLayout(dir); err != nil {
		t.Errorf("clean worksite should pass validation, got: %s", err)
	}
}
