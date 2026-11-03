// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import (
	"strings"
	"testing"
)

// A Group A (command-reverted) resource gets the on_revert-flavored note in
// its generated #~ comment — Option A: visibility via annotation, no enforced
// author-written line.
func TestReversal_CommandTypeGetsAnnotation(t *testing.T) {
	input := `name: m
resources:
  - name: open-port
    type: exec
    command: "true"
    onlyif: "true"
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("lint errors: %v", r.Errors)
	}
	if !strings.Contains(r.Output, "reversible: set revert: true with on_revert:") {
		t.Fatalf("expected command-reversible annotation, got:\n%s", r.Output)
	}
}

// A Group B (value-setter) resource gets the restore-flavored note.
func TestReversal_ValueSetterGetsAnnotation(t *testing.T) {
	input := `name: m
resources:
  - name: set-host
    type: hostname
    hostname: web-01
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("lint errors: %v", r.Errors)
	}
	if !strings.Contains(r.Output, "reversible: set revert: true to restore the prior value") {
		t.Fatalf("expected value-setter annotation, got:\n%s", r.Output)
	}
}

// A non-reversible resource gets no reversal note.
func TestReversal_NonReversibleTypeHasNoAnnotation(t *testing.T) {
	input := `name: m
resources:
  - name: nginx
    type: package
    package: nginx
    state: present
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("lint errors: %v", r.Errors)
	}
	if strings.Contains(r.Output, "reversible:") {
		t.Fatalf("did not expect a reversal annotation, got:\n%s", r.Output)
	}
}
