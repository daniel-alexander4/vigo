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

// TestModuleProvisions_RedundantDeclarationWarns catches the noise:
// a module declares both `provides: [time-sync]` and a `compliance:`
// block listing the same (framework, control) pair the provision
// already covers. The block isn't broken — but the redundancy hides
// the operator's actual intent. Warning, not Error: explicit claims
// remain valid.
func TestModuleProvisions_RedundantDeclarationWarns(t *testing.T) {
	mod := &module{
		Name:     "chrony",
		Provides: []string{"time-sync"},
		Compliance: map[string][]string{
			"pci-dss": {"10.6.1"}, // time-sync covers this
		},
	}
	findings := moduleProvisionsRule{}.apply(mod, Options{})
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
	}
	if findings[0].Severity != Warning {
		t.Errorf("expected Warning severity for redundant claim, got %v", findings[0].Severity)
	}
	if !strings.Contains(findings[0].Message, "redundant") {
		t.Errorf("expected redundant in message, got: %s", findings[0].Message)
	}
	if !strings.Contains(findings[0].Message, "10.6.1") || !strings.Contains(findings[0].Message, "time-sync") {
		t.Errorf("expected (10.6.1, time-sync) in message, got: %s", findings[0].Message)
	}
}

// TestModuleProvisions_ExplicitExtrasAllowed pins the load-bearing
// design point: an explicit compliance block that lists controls
// NOT covered by the provision is fine — operators use this to layer
// extras on top of a provision (e.g. fluentbit using log-shipping
// + extras). No findings for the non-overlap controls.
func TestModuleProvisions_ExplicitExtrasAllowed(t *testing.T) {
	mod := &module{
		Name:     "fluentbit",
		Provides: []string{"log-shipping"},
		Compliance: map[string][]string{
			// log-shipping covers pci-dss [10.5.3]; these are extras
			// that fluentbit legitimately adds.
			"pci-dss":     {"10.2", "10.3"},
			"nist-800-53": {"AU-4", "AU-6"},
		},
	}
	findings := moduleProvisionsRule{}.apply(mod, Options{})
	for _, f := range findings {
		if f.Severity == Error {
			t.Errorf("unexpected Error: %s", f.Message)
		}
		if f.Severity == Warning && strings.Contains(f.Message, "redundant") {
			// A redundant warning would mean one of the explicit
			// controls overlapped — but the test fixture is
			// disjoint by construction.
			t.Errorf("unexpected redundant warning: %s", f.Message)
		}
	}
}

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
