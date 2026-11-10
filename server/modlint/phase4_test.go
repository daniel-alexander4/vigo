// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package modlint

import (
	"strings"
	"testing"
)

// --- when-syntax ---

func TestLint_WhenSyntax_ValidExpressions(t *testing.T) {
	cases := []string{
		``,
		`true`,
		`is_container`,
		`os_family('debian')`,
		`is_root and not is_container`,
		`(is_root or is_admin) and file_exists('/etc/foo')`,
	}
	for _, expr := range cases {
		input := `name: w
resources:
  - name: foo
    type: package
    package: vim
    state: present
    when: "` + expr + `"
`
		r := Lint([]byte(input), Options{})
		if findFinding(r, "when-syntax") != nil {
			t.Errorf("expr %q: expected no when-syntax error; got %v", expr, r.Errors)
		}
	}
}

func TestLint_WhenSyntax_MalformedExpressions(t *testing.T) {
	// We don't pin specific message fragments — the conditionals
	// parser's exact wording isn't part of modlint's contract.
	// What's contractual: a malformed when: produces a when-syntax
	// finding at Error severity. The full err.Error() is included
	// in the message for the operator to read.
	cases := []string{
		`os_family(`,                // unclosed paren / missing arg
		`(is_root and is_container`, // unmatched `(`
		`is_root and`,               // dangling operator
		`os_family('debian)`,        // unterminated string
	}
	for _, expr := range cases {
		input := `name: w
resources:
  - name: foo
    type: package
    package: vim
    state: present
    when: "` + expr + `"
`
		r := Lint([]byte(input), Options{})
		f := findFinding(r, "when-syntax")
		if f == nil {
			t.Errorf("expr %q: expected when-syntax error; got %v", expr, r.Findings)
			continue
		}
		if f.Severity != Error {
			t.Errorf("expr %q: severity=%q, want error", expr, f.Severity)
		}
		if !strings.Contains(f.Message, "malformed") {
			t.Errorf("expr %q: message %q should mention 'malformed'", expr, f.Message)
		}
	}
}

func TestLint_WhenSyntax_GrammarOnlyNotIdentifiers(t *testing.T) {
	// Unknown identifiers (typo'd builtins, missing trait names) lint
	// clean — the rule is grammar-only. Catching these would require
	// the runtime trait set, which lint can't see.
	input := `name: w
resources:
  - name: foo
    type: package
    package: vim
    state: present
    when: "completely_made_up_thing and never_a_real_builtin('x')"
`
	r := Lint([]byte(input), Options{})
	if findFinding(r, "when-syntax") != nil {
		t.Error("when-syntax must be grammar-only — unknown identifiers parse cleanly")
	}
}

// --- template-syntax ---

func TestLint_TemplateSyntax_ValidContent(t *testing.T) {
	input := `name: t
resources:
  - name: motd
    type: file
    state: present
    target_path: /etc/motd
    content: "Hello {{ .Traits.hostname }}, you are running {{ .Vars.role }}."
    owner: root
    group: root
    mode: '0644'
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if findFinding(r, "template-syntax") != nil {
		t.Error("valid template should not trigger template-syntax")
	}
}

func TestLint_TemplateSyntax_NoTemplateMarkers(t *testing.T) {
	// content with no {{ should be skipped entirely (no parse).
	input := `name: t
resources:
  - name: hello
    type: file
    state: present
    target_path: /etc/hello
    content: "plain text with no template markers"
    owner: root
    group: root
    mode: '0644'
`
	r := Lint([]byte(input), Options{})
	if findFinding(r, "template-syntax") != nil {
		t.Error("plain string must skip template-syntax")
	}
}

func TestLint_TemplateSyntax_UnclosedAction(t *testing.T) {
	input := `name: t
resources:
  - name: motd
    type: file
    state: present
    target_path: /etc/motd
    content: "Hello {{ .Traits.hostname"
    owner: root
    group: root
    mode: '0644'
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected template-syntax error for unclosed {{")
	}
	if !containsError(r, "template syntax error") {
		t.Errorf("expected template-syntax error; got %v", r.Errors)
	}
}

func TestLint_TemplateSyntax_UnmatchedIfEnd(t *testing.T) {
	input := `name: t
resources:
  - name: conf
    type: file
    state: present
    target_path: /etc/conf
    content: "{{ if .Vars.x }}value\n"
    owner: root
    group: root
    mode: '0644'
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected template-syntax error for {{ if }} without {{ end }}")
	}
	if !containsError(r, "template syntax error") {
		t.Errorf("expected template-syntax error; got %v", r.Errors)
	}
}

func TestLint_TemplateSyntax_GrammarOnlyNotFuncs(t *testing.T) {
	// Unknown function references parse fine (funcMap is execute-time).
	// We're catching syntax errors, not undefined-function errors.
	input := `name: t
resources:
  - name: conf
    type: file
    state: present
    target_path: /etc/conf
    content: "{{ definitelyNotARealFunction .X }}"
    owner: root
    group: root
    mode: '0644'
`
	r := Lint([]byte(input), Options{})
	if findFinding(r, "template-syntax") != nil {
		t.Error("template-syntax must be grammar-only — unknown funcs parse cleanly")
	}
}
