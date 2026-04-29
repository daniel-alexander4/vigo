// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"

	"git.alexander4.io/vigo/server/conditionals"
)

// whenSyntaxRule parses every resource's `when:` expression at lint
// time so syntax errors fail the publish instead of the agent.
//
// Without this check, a typo like `os_familly('debian')` (extra l)
// or a missing closing paren rides through to the agent, where the
// parser fails during convergence and the resource is silently
// skipped — the operator sees a converged module producing no
// effect and has to chase the failure through agent logs.
//
// Reuses `server/conditionals.ParseWhenExpr` so modlint and the
// agent agree on grammar by construction. The conditionals package
// is stdlib-only (fmt, strings, unicode), keeping modlint
// cloud-function-deployable.
//
// Empty `when:` is fine — ParseWhenExpr returns BoolLitExpr{true}
// (always run). Unknown identifiers also pass: this rule is
// grammar-only, mirroring the agent's compile-time behavior.
// A typo'd builtin like `file_exsists` parses as a function call
// that's just never going to evaluate true — caught at runtime.
// The harder errors — unmatched parens, dangling operators,
// unterminated strings — are what this rule actually catches.
type whenSyntaxRule struct{}

func (whenSyntaxRule) id() string { return "when-syntax" }

func (whenSyntaxRule) apply(mod *module, _ Options) []Finding {
	var out []Finding
	for _, res := range mod.Resources {
		if res.When == "" {
			continue
		}
		if _, err := conditionals.ParseWhenExpr(res.When); err != nil {
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "when-syntax",
				Message: fmt.Sprintf(
					"resource %q: when: expression is malformed — %s",
					res.Name, err.Error(),
				),
			})
		}
	}
	return out
}

func init() { register(whenSyntaxRule{}) }
