// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

// noResourcesRule rejects a module that declares no resources. A module
// without resources is almost always a half-finished file — the operator
// pasted the header and walked away — and applying it to the fleet is a
// no-op that wastes a publish cycle.
type noResourcesRule struct{}

func (noResourcesRule) id() string { return "no-resources" }

func (noResourcesRule) apply(mod *module, _ Options) []Finding {
	if len(mod.Resources) > 0 {
		return nil
	}
	return []Finding{{
		Severity: Error,
		Line:     mod.Line,
		Column:   mod.Column,
		RuleID:   "no-resources",
		Message:  "module has no resources",
	}}
}

func init() { register(noResourcesRule{}) }
