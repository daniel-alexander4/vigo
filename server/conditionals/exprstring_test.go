// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package conditionals

import (
	"reflect"
	"testing"
)

// TestExprStringRoundTrips: ExprString output re-parses to the same AST,
// including a negated comparison and string literals holding quotes and
// backslashes.
func TestExprStringRoundTrips(t *testing.T) {
	asts := []WhenExpr{
		&NotWhenExpr{Operand: &EqExpr{Left: &VarRefExpr{Name: "a"}, Right: &StringLitExpr{Value: "b"}}},
		&NotWhenExpr{Operand: &NeqExpr{Left: &VarRefExpr{Name: "a"}, Right: &StringLitExpr{Value: "b"}}},
		&EqExpr{Left: &VarRefExpr{Name: "os"}, Right: &StringLitExpr{Value: "it's"}},
		&EqExpr{Left: &VarRefExpr{Name: "p"}, Right: &StringLitExpr{Value: `C:\dir\`}},
		&FuncCallExpr{Name: "os_family", Args: []string{"de'b", `x\y`}},
		&EqExpr{Left: &NotWhenExpr{Operand: &VarRefExpr{Name: "a"}}, Right: &BoolLitExpr{Value: true}},
		&EqExpr{Left: &OrWhenExpr{Left: &VarRefExpr{Name: "a"}, Right: &VarRefExpr{Name: "b"}}, Right: &BoolLitExpr{Value: true}},
		&AndWhenExpr{Left: &OrWhenExpr{Left: &VarRefExpr{Name: "a"}, Right: &VarRefExpr{Name: "b"}}, Right: &EqExpr{Left: &VarRefExpr{Name: "c"}, Right: &StringLitExpr{Value: "d"}}},
	}
	for _, want := range asts {
		s := ExprString(want)
		got, err := ParseWhenExpr(s)
		if err != nil {
			t.Errorf("ExprString = %q does not parse: %v", s, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ExprString = %q re-parses to %#v, want %#v", s, got, want)
		}
	}
}

// TestLexStringUnescapes: an escaped quote in a literal is a quote in the
// value, not a backslash-quote pair.
func TestLexStringUnescapes(t *testing.T) {
	got, err := ParseWhenExpr(`name == 'it\'s'`)
	if err != nil {
		t.Fatal(err)
	}
	eq, ok := got.(*EqExpr)
	if !ok {
		t.Fatalf("parsed %#v", got)
	}
	if lit, _ := eq.Right.(*StringLitExpr); lit == nil || lit.Value != "it's" {
		t.Fatalf("literal = %#v, want it's", eq.Right)
	}
}
