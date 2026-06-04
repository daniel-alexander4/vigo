// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package validate

import (
	"strings"
	"testing"
)

func hasMessageContaining(findings []Finding, substr string) bool {
	for _, f := range findings {
		if strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

// Group A (command-reverted) types must declare on_revert: when revert: true —
// their inverse can't be inferred. Caught at publish.
func TestReversal_CommandRevertWithoutOnRevert_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "m",
		Resources: []Resource{
			{Name: "open-port", Type: "exec", Attributes: map[string]any{
				"command": "iptables -A INPUT -p tcp --dport 8080 -j ACCEPT",
				"onlyif":  "true", // satisfy the idempotency-guard rule
				"revert":  true,
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !hasMessageContaining(findings, "no on_revert") {
		t.Fatalf("expected on_revert-required error, got: %+v", findings)
	}
}

func TestReversal_CommandRevertWithOnRevert_IsClean(t *testing.T) {
	mod := Configcrate{
		Name: "m",
		Resources: []Resource{
			{Name: "open-port", Type: "exec", Attributes: map[string]any{
				"command":   "iptables -A INPUT -p tcp --dport 8080 -j ACCEPT",
				"onlyif":    "true",
				"revert":    true,
				"on_revert": "iptables -D INPUT -p tcp --dport 8080 -j ACCEPT",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if hasMessageContaining(findings, "on_revert") {
		t.Fatalf("did not expect a reversal error, got: %+v", findings)
	}
}

// Group B (value-setters) infer their inverse from a snapshot — revert: true
// needs no on_revert:.
func TestReversal_ValueSetterRevertNeedsNoOnRevert(t *testing.T) {
	mod := Configcrate{
		Name: "m",
		Resources: []Resource{
			{Name: "set-host", Type: "hostname", Attributes: map[string]any{
				"hostname": "web-01",
				"revert":   true,
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Fatalf("value-setter revert should be clean without on_revert, got: %+v", findings)
	}
}

// revert/on_revert are known attrs on reversible types — never "unknown".
func TestReversal_AttrsAreKnownOnReversibleTypes(t *testing.T) {
	mod := Configcrate{
		Name: "m",
		Resources: []Resource{
			{Name: "set-host", Type: "hostname", Attributes: map[string]any{
				"hostname": "web-01", "revert": false,
			}},
			{Name: "run", Type: "exec", Attributes: map[string]any{
				"command": "true", "onlyif": "true",
				"revert": false, "on_revert": "true",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if hasMessageContaining(findings, "unknown attribute") {
		t.Fatalf("revert/on_revert should be known on reversible types, got: %+v", findings)
	}
}

// A non-reversible type does not gain revert/on_revert — they flag as unknown.
func TestReversal_NonReversibleTypeRejectsRevert(t *testing.T) {
	mod := Configcrate{
		Name: "m",
		Resources: []Resource{
			{Name: "nginx", Type: "package", Attributes: map[string]any{
				"package": "nginx", "state": "present", "revert": true,
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !hasMessageContaining(findings, "unknown attribute") {
		t.Fatalf("expected revert to be unknown on a non-reversible type, got: %+v", findings)
	}
}

// CommandReversibleTypes is a subset of ReversibleTypes — guards the SoT shape
// the agent (agent/src/runner/reversal.rs) mirrors.
func TestReversal_CommandSetIsSubsetOfReversible(t *testing.T) {
	for typ := range CommandReversibleTypes {
		if !ReversibleTypes[typ] {
			t.Errorf("%q is command-reversible but not in ReversibleTypes", typ)
		}
	}
}
