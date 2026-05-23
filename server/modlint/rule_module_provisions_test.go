// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"strings"
	"testing"
)

// TestModuleProvisions_KnownTagPasses pins the happy path: a module
// declaring a provision the catalog knows about produces no findings.
func TestModuleProvisions_KnownTagPasses(t *testing.T) {
	mod := &module{
		Name:     "chrony",
		Provides: []string{"time-sync"},
	}
	findings := moduleProvisionsRule{}.apply(mod, Options{})
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %v", findings)
	}
}

// TestModuleProvisions_UnknownTagWithDidYouMean catches the failure
// mode the rule exists to prevent: a typo'd provision tag silently
// resolves to nil at runtime (loader treats unknowns as zero
// contribution) — coverage drops, audit catches it months later. The
// rule produces an Error with a did-you-mean suggestion when the typo
// is close to a known tag.
func TestModuleProvisions_UnknownTagWithDidYouMean(t *testing.T) {
	mod := &module{
		Name:     "chrony",
		Provides: []string{"time-syc"}, // typo: missing 'n'
	}
	findings := moduleProvisionsRule{}.apply(mod, Options{})
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
	}
	if findings[0].Severity != Error {
		t.Errorf("expected Error severity, got %v", findings[0].Severity)
	}
	if !strings.Contains(findings[0].Message, "time-sync") {
		t.Errorf("expected did-you-mean suggestion of time-sync, got: %s", findings[0].Message)
	}
}

// Redundancy-warning + explicit-extras tests retired with the
// per-module `compliance:` key in 0.66.48 —
// moduleComplianceKeyRetiredRule is the single error path now, and
// `provides:` no longer interacts with an explicit `compliance:` block
// at the module level (only at the directory-inheritance level via
// `compliance.vgo`, which isn't a module YAML and doesn't go through
// this rule).

// TestModuleProvisions_NoProvidesNoFindings pins that the rule is a
// no-op for modules without a provides: field — existing modules with
// only `compliance:` blocks aren't disturbed.
func TestModuleProvisions_NoProvidesNoFindings(t *testing.T) {
	mod := &module{
		Name: "legacy-module",
		Compliance: map[string][]string{
			"pci-dss": {"10.6.1"},
		},
	}
	findings := moduleProvisionsRule{}.apply(mod, Options{})
	if len(findings) != 0 {
		t.Errorf("expected no findings for module without provides:, got %v", findings)
	}
}
