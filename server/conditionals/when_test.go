// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package conditionals

import "testing"

// Symbol-form operators (`!`, `&&`, `||`) must lex and parse identically to
// their keyword equivalents (`not`, `and`, `or`). Without this, modlint
// rejects valid operator content that the server-side evaluator
// (server/config/template.go) and the Rust agent parser both accept.
func TestParseWhenExpr_SymbolOperators(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"bang_function", "!os_family('debian')"},
		{"bang_ident", "!is_container"},
		{"and_symbols", "os_family('redhat') && has_command('dnf')"},
		{"or_symbols", "os('debian') || os('ubuntu')"},
		{"mixed_bang_and", "os_family('redhat') && !has_command('dnf')"},
		{"chained_or", "os('arch') || os('debian') || os('rhel')"},
		{"parens_with_symbols", "(os('a') || os('b')) && !is_container"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseWhenExpr(tc.expr); err != nil {
				t.Fatalf("ParseWhenExpr(%q) failed: %v", tc.expr, err)
			}
		})
	}
}

// Single `&` and single `|` are not valid — the lexer must produce a
// pointed error rather than silently accepting them.
func TestParseWhenExpr_SingleAmpOrPipeRejected(t *testing.T) {
	for _, expr := range []string{
		"a & b",
		"a | b",
		"!a & b",
	} {
		if _, err := ParseWhenExpr(expr); err == nil {
			t.Errorf("ParseWhenExpr(%q) should have failed but succeeded", expr)
		}
	}
}

// String comparison operators (`==`, `!=`) parse with operands that can be
// string literals, variable references, or a mix. Bare string literals are
// only legal in comparison context — outside, EvalWhenExpr errors.
func TestParseWhenExpr_ComparisonOperators(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"literal_eq_literal", "'a' == 'b'"},
		{"literal_neq_literal", "'a' != 'b'"},
		{"var_neq_empty", "release != ''"},
		{"empty_neq_var", "'' != release"},
		{"templated_var_neq_empty", "'{{ .Vars.target_release }}' != ''"},
		{"comparison_in_and", "os('debian') && release != ''"},
		{"comparison_in_or", "release == 'jammy' || release == 'noble'"},
		{"and_binds_tighter", "a == 'x' && b == 'y'"}, // (a=='x') && (b=='y')
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseWhenExpr(tc.expr); err != nil {
				t.Fatalf("ParseWhenExpr(%q) failed: %v", tc.expr, err)
			}
		})
	}
}

// Comparison chaining is rejected — operators don't compose into a
// trichotomy in this grammar.
func TestParseWhenExpr_ComparisonChainingRejected(t *testing.T) {
	for _, expr := range []string{
		"a == b == c",
		"a != b != c",
		"a == b != c",
	} {
		if _, err := ParseWhenExpr(expr); err == nil {
			t.Errorf("ParseWhenExpr(%q) should have failed but succeeded", expr)
		}
	}
}

// Single `=` is invalid — operators must use `==` / `!=`.
func TestParseWhenExpr_SingleEqualsRejected(t *testing.T) {
	if _, err := ParseWhenExpr("a = 'b'"); err == nil {
		t.Error("ParseWhenExpr(\"a = 'b'\") should have failed")
	}
}

// SubstituteVars in comparison context resolves VarRefExpr operands to
// StringLitExpr based on the actual string value of the var, NOT to
// BoolLitExpr (which is the contract outside comparison context).
// TristateEval can then resolve the comparison fully.
func TestSubstituteVars_StringComparisonContext(t *testing.T) {
	expr, err := ParseWhenExpr("release != ''")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	substituted := SubstituteVars(expr, map[string]any{"release": "jammy"})
	result := TristateEval(substituted)
	if result == nil || *result != true {
		t.Errorf("expected true (jammy != ''), got %v", result)
	}

	substituted2 := SubstituteVars(expr, map[string]any{"release": ""})
	result2 := TristateEval(substituted2)
	if result2 == nil || *result2 != false {
		t.Errorf("expected false ('' != ''), got %v", result2)
	}
}

// Symbol and keyword forms must produce equivalent ASTs so the rest of the
// pipeline (SubstituteVars, EvalWhenExpr) doesn't have to care which form
// the operator typed. Round-trip via Eval with a no-op resolver.
func TestParseWhenExpr_SymbolKeywordEquivalence(t *testing.T) {
	pairs := []struct{ symbolic, keyword string }{
		{"!a", "not a"},
		{"a && b", "a and b"},
		{"a || b", "a or b"},
		{"!a && (b || c)", "not a and (b or c)"},
	}
	resolve := func(name string) bool {
		// Any var name returns true so we exercise the operators not the
		// var lookup.
		return true
	}
	for _, p := range pairs {
		sym, errS := ParseWhenExpr(p.symbolic)
		kw, errK := ParseWhenExpr(p.keyword)
		if errS != nil || errK != nil {
			t.Fatalf("parse: symbolic=%v keyword=%v", errS, errK)
		}
		gotSym, _ := EvalWhenExpr(sym, resolve)
		gotKw, _ := EvalWhenExpr(kw, resolve)
		if gotSym != gotKw {
			t.Errorf("symbol %q (=%v) and keyword %q (=%v) must evaluate the same",
				p.symbolic, gotSym, p.keyword, gotKw)
		}
	}
}
