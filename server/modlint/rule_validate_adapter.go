// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import (
	vigovalidate "git.alexander4.io/vigo/server/validate"
)

// validateAdapterRule wraps `server/validate.ValidateConfigcrate` so its
// type-specific and cross-reference findings (unknown types, missing
// required attrs, depends_on cycles, idempotency anti-patterns the
// canonical validator already knows about) flow through modlint's
// rule registry alongside everything else.
//
// The validate package and modlint share the same KnownTypes table —
// having one canonical validator avoids the modlint/server skew that
// would otherwise force operators to debug "passed lint, rejected at
// publish" cycles.
//
// Source positions: vigovalidate.Finding doesn't carry Line/Column,
// so the adapter looks up the line of the named resource in `mod`
// when it can. Module-level findings (no resource name) come through
// at line 0.
type validateAdapterRule struct{}

func (validateAdapterRule) id() string { return "validate-adapter" }

func (validateAdapterRule) apply(mod *configcrate, _ Options) []Finding {
	vm := toValidateConfigcrate(mod)
	findings := vigovalidate.ValidateConfigcrate(vm)

	// Build a name → line index once for O(1) lookup.
	resLine := make(map[string]int, len(mod.Resources))
	resCol := make(map[string]int, len(mod.Resources))
	for _, r := range mod.Resources {
		if r.Name != "" {
			resLine[r.Name] = r.Line
			resCol[r.Name] = r.Column
		}
	}

	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		sev := Warning
		if f.Severity == vigovalidate.SeverityError {
			sev = Error
		}
		out = append(out, Finding{
			Severity: sev,
			Line:     resLine[f.ResourceName], // 0 if not found / module-level
			Column:   resCol[f.ResourceName],
			RuleID:   "validate-adapter",
			Message:  f.Message,
		})
	}
	return out
}

func init() { register(validateAdapterRule{}) }
