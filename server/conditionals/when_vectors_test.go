// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package conditionals

import (
	"encoding/json"
	"os"
	"testing"
)

// TestWhenCrossLanguageVectors: an expression the server cannot fully
// resolve is printed (ExprString) and re-parsed by the agent
// (agent/src/policy/when.rs). The agent once lacked `==`/`!=` and kept
// backslashes in string literals, so the two sides disagreed. Both run
// testdata/when_vectors.json: parse, substitute vars, evaluate; `canon` is
// the printed form both printers must produce.
func TestWhenCrossLanguageVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/when_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Expr  string         `json:"expr"`
		Vars  map[string]any `json:"vars"`
		Want  any            `json:"want"`
		Canon string         `json:"canon"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		expr, err := ParseWhenExpr(c.Expr)
		if c.Want == "parse_error" {
			if err == nil {
				t.Errorf("%s: parsed, want a parse error", c.Expr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Expr, err)
			continue
		}
		if c.Canon != "" {
			if got := ExprString(expr); got != c.Canon {
				t.Errorf("%s: printed %q, want %q", c.Expr, got, c.Canon)
			}
		}
		got, err := EvalWhenExpr(SubstituteVars(expr, c.Vars), func(string) bool { return false })
		switch want := c.Want.(type) {
		case bool:
			if err != nil || got != want {
				t.Errorf("%s: got %v (err %v), want %v", c.Expr, got, err, want)
			}
		default:
			if err == nil {
				t.Errorf("%s: evaluated to %v, want an eval error", c.Expr, got)
			}
		}
	}
}
