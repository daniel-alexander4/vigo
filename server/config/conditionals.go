// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"strings"

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
		return conditionals.WhenVarIsTrue(name, vars)
	})
	if err != nil {
		return false
	}
	return result
}

// serverSideBuiltins are functions the server can evaluate using traits.
// Agent-only functions (has_command, file_exists, etc.) are not in this set.
var serverSideBuiltins = map[string]bool{
	"os_family":   true,
	"distro":      true,
	"arch":        true,
	"os":          true,
	"version_ge":  true,
	"has_display": true,
}

// isFullyServerResolvable returns true if all function calls in the when
// expression can be evaluated server-side. If any agent-only function is
// present (has_command, file_exists, etc.), returns false.
func isFullyServerResolvable(when string) bool {
	// Scan for function calls: name(
	rest := when
	for {
		idx := strings.Index(rest, "(")
		if idx <= 0 {
			return true
		}
		// Extract function name (alphanumeric + underscore before the paren).
		nameStart := idx - 1
		for nameStart >= 0 && (rest[nameStart] == '_' || (rest[nameStart] >= 'a' && rest[nameStart] <= 'z') || (rest[nameStart] >= 'A' && rest[nameStart] <= 'Z') || (rest[nameStart] >= '0' && rest[nameStart] <= '9')) {
			nameStart--
		}
		funcName := rest[nameStart+1 : idx]
		if funcName != "" && !serverSideBuiltins[funcName] {
			return false
		}
		rest = rest[idx+1:]
	}
}

// EvalModuleWhenServerSide evaluates a module-level when: expression using
// traits. Handles builtin functions (os_family, distro, arch, etc.) and
// boolean operators (and/or/&&/||/!/not). Returns true if the module should
// be included.
//
// For compound expressions (&&/||), each part is evaluated independently:
//   - && (and): if ANY server-resolvable part is false, the whole expression
//     is false (short-circuit). Remaining agent-only parts are passed through.
//   - || (or): if ANY server-resolvable part is true, the whole expression
//     is true (short-circuit). Remaining agent-only parts are dropped.
func EvalModuleWhenServerSide(when string, traits map[string]any) (include bool, agentWhen string) {
	if when == "" {
		return true, ""
	}

	// If fully server-resolvable, evaluate directly.
	if isFullyServerResolvable(when) {
		return evalBuiltinsServerSide(when, traits), ""
	}

	// Mixed expression — try to short-circuit on && and ||.
	// Agent parser uses "and"/"or" keywords (not &&/||), so remaining
	// parts are always joined with " and " / " or ".
	for _, sep := range []string{" && ", " and "} {
		if strings.Contains(when, sep) {
			parts := strings.Split(when, sep)
			var remaining []string
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if isFullyServerResolvable(part) {
					if !evalBuiltinsServerSide(part, traits) {
						return false, "" // short-circuit: one false → all false
					}
				} else {
					remaining = append(remaining, part)
				}
			}
			if len(remaining) == 0 {
				return true, ""
			}
			return true, strings.Join(remaining, " and ")
		}
	}

	for _, sep := range []string{" || ", " or "} {
		if strings.Contains(when, sep) {
			parts := strings.Split(when, sep)
			var remaining []string
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if isFullyServerResolvable(part) {
					if evalBuiltinsServerSide(part, traits) {
						return true, "" // short-circuit: one true → all true
					}
				} else {
					remaining = append(remaining, part)
				}
			}
			if len(remaining) == 0 {
				return false, ""
			}
			return true, strings.Join(remaining, " or ")
		}
	}

	// Single agent-only function — pass through.
	return true, when
}
