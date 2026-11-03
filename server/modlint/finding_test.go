// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import (
	"strings"
	"testing"
)

// TestLint_FindingsCarryRuleAndLine confirms Phase 1's structured
// output: every emitted Finding has a Severity and a RuleID, and
// resource-level findings carry the line number of the offending
// resource — not 0.
func TestLint_FindingsCarryRuleAndLine(t *testing.T) {
	// Two resources sharing a name. The duplicate-resource-names rule
	// should anchor its finding to the second occurrence (line 7).
	input := `name: dup
resources:
  - name: same
    type: package
    state: present
    package: foo
  - name: same
    type: package
    state: present
    package: bar
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected duplicate-name error")
	}
	var dup *Finding
	for i := range r.Findings {
		if r.Findings[i].RuleID == "duplicate-resource-names" {
			dup = &r.Findings[i]
			break
		}
	}
	if dup == nil {
		t.Fatalf("no duplicate-resource-names finding in %v", r.Findings)
	}
	if dup.Severity != Error {
		t.Errorf("severity = %q, want %q", dup.Severity, Error)
	}
	if dup.Line < 6 || dup.Line > 8 {
		t.Errorf("Line = %d, want ~7 (the second `- name: same`)", dup.Line)
	}
	if !strings.Contains(dup.Message, "same") {
		t.Errorf("Message = %q, want it to mention 'same'", dup.Message)
	}
}

// TestLint_FixAppliedOnAutoFix confirms the auto-fix rule (service
// with subscribes but no when:"changed") sets FixApplied=true so a
// JSON consumer can render it as a notice rather than as something
// the operator must act on.
func TestLint_FixAppliedOnAutoFix(t *testing.T) {
	input := `name: svc
resources:
  - name: nginx-conf
    type: file
    state: present
    target_path: /etc/nginx/nginx.conf
    content: "..."
    owner: root
    group: root
    mode: '0644'
  - name: nginx
    type: service
    service: nginx
    state: restarted
    subscribes: [nginx-conf]
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got %v", r.Errors)
	}
	var fix *Finding
	for i := range r.Findings {
		if r.Findings[i].RuleID == "service-subscribes-when-changed" {
			fix = &r.Findings[i]
			break
		}
	}
	if fix == nil {
		t.Fatalf("expected service-subscribes-when-changed finding, got %v", r.Findings)
	}
	if !fix.FixApplied {
		t.Error("FixApplied should be true on auto-fix")
	}
	if fix.Severity != Warning {
		t.Errorf("severity = %q, want %q", fix.Severity, Warning)
	}
	if fix.Line == 0 {
		t.Error("Line should be set for a resource-level finding")
	}
}

// TestLint_LegacyErrorsWarningsStillPopulated confirms the
// backward-compatibility shim: callers that read Result.Errors and
// Result.Warnings directly continue to see the same string slices
// after the Phase 1 refactor.
func TestLint_LegacyErrorsWarningsStillPopulated(t *testing.T) {
	input := `resources:
  - name: foo
    type: package
    state: present
    package: vim
`
	r := Lint([]byte(input), Options{})
	// Missing name → one Error finding → Errors slice has 1 entry.
	if len(r.Errors) != 1 {
		t.Fatalf("Errors len = %d, want 1 (%v)", len(r.Errors), r.Errors)
	}
	if !strings.Contains(r.Errors[0], "name") {
		t.Errorf("Errors[0] = %q, want it to mention 'name'", r.Errors[0])
	}
	// Findings carries the same content with structure.
	if len(r.Findings) == 0 {
		t.Fatal("Findings should have at least one entry")
	}
}
