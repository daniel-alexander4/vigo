// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package modlint

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestResult_JSONSchema pins the public JSON schema so external
// consumers (CI tools, the community-upload Cloud Function, vigocli
// output filters) can rely on it. Field renames or removals here
// must be a deliberate breaking-change decision, not an accidental
// drive-by edit.
func TestResult_JSONSchema(t *testing.T) {
	r := &Result{
		Output: "name: ok\n",
		Findings: []Finding{
			{
				Severity:   Error,
				Line:       3,
				Column:     5,
				RuleID:     "duplicate-resource-names",
				Message:    `duplicate resource name "x"`,
				FixApplied: false,
			},
			{
				Severity:   Warning,
				Line:       1,
				Column:     0,
				RuleID:     "mode-octal",
				Message:    "auto-quoted to 0644",
				FixApplied: true,
			},
		},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		`"output":"name: ok\n"`,
		`"findings":[`,
		`"severity":"error"`,
		`"severity":"warning"`,
		`"line":3`,
		`"column":5`,
		`"rule_id":"duplicate-resource-names"`,
		`"message":"duplicate resource name \"x\""`,
		`"fix_applied":true`, // present when true
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in JSON output:\n%s", want, got)
		}
	}
	// Legacy slices must NOT appear in JSON — Findings is canonical.
	for _, forbid := range []string{`"Errors":`, `"Warnings":`, `"errors":`, `"warnings":`} {
		if strings.Contains(got, forbid) {
			t.Errorf("legacy field %q leaked into JSON output:\n%s", forbid, got)
		}
	}
}

// TestFinding_OmitsZeroLineColumn verifies that line=0, column=0
// findings (module-level, no source location) don't pollute the JSON
// with `"line":0,"column":0` noise. The omitempty tags handle this.
func TestFinding_OmitsZeroLineColumn(t *testing.T) {
	f := Finding{
		Severity: Error,
		RuleID:   "no-resources",
		Message:  "module has no resources",
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if strings.Contains(got, `"line":0`) || strings.Contains(got, `"column":0`) {
		t.Errorf("zero line/column should be omitted; got %s", got)
	}
}

// TestSeverity_StringRoundTrip confirms Severity stays a string in
// JSON (not an int) — the schema's stability hinges on this.
func TestSeverity_StringRoundTrip(t *testing.T) {
	for _, s := range []Severity{Error, Warning, Info, Hint} {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal %q: %v", s, err)
		}
		want := `"` + string(s) + `"`
		if string(b) != want {
			t.Errorf("Severity %q marshaled to %s, want %s", s, b, want)
		}
		var back Severity
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if back != s {
			t.Errorf("round-trip drift: %q → %q", s, back)
		}
	}
}
