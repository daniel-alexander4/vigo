package config

import (
	"fmt"

	"github.com/hobnob/hobnob/server/conditionals"
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
			return whenVarIsTrue(name, vars)
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

// ShouldIncludeModule evaluates a module's When expression against the
// resolved vars map. Returns true if the module should be included.
func ShouldIncludeModule(mod ModuleConfig, vars map[string]any) bool {
	if mod.When == "" {
		return true
	}

	expr, err := conditionals.ParseWhenExpr(mod.When)
	if err != nil {
		return false
	}

	result, err := conditionals.EvalWhenExpr(expr, func(name string) bool {
		return whenVarIsTrue(name, vars)
	})
	if err != nil {
		return false
	}
	return result
}

// whenVarIsTrue returns true only if vars[name] == "true" (string).
// Unknown var names or non-"true" values return false.
func whenVarIsTrue(name string, vars map[string]any) bool {
	v, ok := vars[name]
	if !ok {
		return false
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	return s == "true"
}
