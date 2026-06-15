// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"

	"git.alexander4.io/vigo/server/conditionals"
)

// EvaluateResourceWhen evaluates a resource's When expression against the
// resolved vars map. It handles three cases:
//
//  1. Empty when → always include (true, "", nil)
//  2. No builtin function calls → full server-side eval → (result, "", nil)
//  3. Has builtin function calls → substitute vars, try short-circuit →
//     if deterministic: (result, "", nil)
//     if still unknown: (true, substitutedExpr, nil) — pass to agent
func EvaluateResourceWhen(when string, vars map[string]any) (shouldInclude bool, agentWhen string, err error) {
	if when == "" {
		return true, "", nil
	}

	expr, err := conditionals.ParseWhenExpr(when)
	if err != nil {
		return false, "", fmt.Errorf("parsing when expression %q: %w", when, err)
	}

	if !conditionals.ContainsFuncCalls(expr) {
		// Pure var expression — evaluate fully server-side.
		result, err := conditionals.EvalWhenExpr(expr, func(name string) bool {
			return conditionals.WhenVarIsTrue(name, vars)
		})
		if err != nil {
			return false, "", fmt.Errorf("evaluating when expression %q: %w", when, err)
		}
		return result, "", nil
	}

	// Expression contains builtins — substitute vars, try short-circuit.
	substituted := conditionals.SubstituteVars(expr, vars)
	result := conditionals.TristateEval(substituted)
	if result != nil {
		return *result, "", nil
	}

	// Can't determine server-side — pass to agent.
	return true, conditionals.ExprString(substituted), nil
}

// ShouldIncludeConfigcrate evaluates a module's When expression against the
// resolved vars map. Returns true if the module should be included.
func ShouldIncludeConfigcrate(mod Configcrate, vars map[string]any) bool {
	if mod.When == "" {
		return true
	}

	expr, err := conditionals.ParseWhenExpr(mod.When)
	if err != nil {
		return false
	}

	result, err := conditionals.EvalWhenExpr(expr, func(name string) bool {
		return conditionals.WhenVarIsTrue(name, vars)
	})
	if err != nil {
		return false
	}
	return result
}

// serverSideBuiltins are functions the server can evaluate using traits.
// Agent-only functions (has_command, file_exists, etc.) are not in this set.
// This must stay in lockstep with the cases evalTraitFunc handles.
var serverSideBuiltins = map[string]bool{
	"os_family":   true,
	"distro":      true,
	"arch":        true,
	"os":          true,
	"version_ge":  true,
	"has_display": true,
}

// EvalConfigcrateWhenServerSide evaluates a module- or resource-level when:
// expression server-side against the envoy's traits. It routes through the
// canonical AST parser in server/conditionals (the same parser the agent
// uses) so server and agent agree on precedence, parentheses, and operator
// semantics — the previous string-split evaluator diverged on all three.
//
// Semantics preserved exactly:
//   - Server-resolvable builtins (os_family/distro/arch/os/version_ge/
//     has_display) are evaluated here against traits.
//   - Agent-only builtins (has_command, file_exists, …) are left unknown.
//   - If the whole expression reduces to a definite value, return
//     (result, "") — the module/resource is included or dropped here.
//   - If trait-dependent parts remain unknown after short-circuit reduction,
//     return (true, <residual expression>) so the agent re-evaluates the
//     remainder against its own traits — exactly as before.
//
// A parse error is treated conservatively as "defer to the agent" (include
// with the original expression), so a malformed expression can never silently
// drop a module server-side; the agent (and publish-time validation) surface
// the error.
func EvalConfigcrateWhenServerSide(when string, traits map[string]any) (include bool, agentWhen string) {
	if when == "" {
		return true, ""
	}

	expr, err := conditionals.ParseWhenExpr(when)
	if err != nil {
		return true, when
	}

	// Reduce server-resolvable function calls against traits; agent-only
	// calls and any var refs are left in place for TristateEval to mark
	// unknown.
	reduced := reduceServerSideFuncs(expr, traits)

	if result := conditionals.TristateEval(reduced); result != nil {
		return *result, ""
	}

	// Some trait-dependent (agent-only) part remains — pass the residual
	// expression through for the agent to finish. ExprString renders the
	// canonical form (keywords, parens where needed) the agent parser reads.
	return true, conditionals.ExprString(reduced)
}

// reduceServerSideFuncs walks a when AST and replaces every
// server-resolvable builtin FuncCallExpr with a concrete BoolLitExpr
// (evaluated against traits). Agent-only builtins, var refs, and string
// literals are left untouched so TristateEval can treat them as unknown.
func reduceServerSideFuncs(expr conditionals.WhenExpr, traits map[string]any) conditionals.WhenExpr {
	switch e := expr.(type) {
	case *conditionals.FuncCallExpr:
		if serverSideBuiltins[e.Name] {
			arg := ""
			if len(e.Args) > 0 {
				arg = e.Args[0]
			}
			return &conditionals.BoolLitExpr{Value: evalTraitFunc(e.Name, arg, traits)}
		}
		return e
	case *conditionals.NotWhenExpr:
		return &conditionals.NotWhenExpr{Operand: reduceServerSideFuncs(e.Operand, traits)}
	case *conditionals.AndWhenExpr:
		return &conditionals.AndWhenExpr{
			Left:  reduceServerSideFuncs(e.Left, traits),
			Right: reduceServerSideFuncs(e.Right, traits),
		}
	case *conditionals.OrWhenExpr:
		return &conditionals.OrWhenExpr{
			Left:  reduceServerSideFuncs(e.Left, traits),
			Right: reduceServerSideFuncs(e.Right, traits),
		}
	case *conditionals.EqExpr:
		return &conditionals.EqExpr{
			Left:  reduceServerSideFuncs(e.Left, traits),
			Right: reduceServerSideFuncs(e.Right, traits),
		}
	case *conditionals.NeqExpr:
		return &conditionals.NeqExpr{
			Left:  reduceServerSideFuncs(e.Left, traits),
			Right: reduceServerSideFuncs(e.Right, traits),
		}
	default:
		// BoolLitExpr, VarRefExpr, StringLitExpr — pass through unchanged.
		return expr
	}
}
