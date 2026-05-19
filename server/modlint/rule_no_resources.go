// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import "strings"

// noResourcesRule rejects a module that declares no resources. A module
// without resources is almost always a half-finished file — the operator
// pasted the header and walked away — and applying it to the fleet is a
// no-op that wastes a publish cycle.
//
// Exception: retract templates (name ends with "-retract") may legitimately
// have no resources when there's nothing the agent can safely auto-undo
// (e.g. CIS Windows security policy values, network-device SSH configs,
// audit settings without a known prior value). For those, the retract is a
// documentation surface — the comments tell the operator what to clean up
// by hand. The rule still warns so a *half-finished* retract is visible,
// but it doesn't block publish.
type noResourcesRule struct{}

func (noResourcesRule) id() string { return "no-resources" }

func (noResourcesRule) apply(mod *module, _ Options) []Finding {
	if len(mod.Resources) > 0 {
		return nil
	}
	severity := Error
	if strings.HasSuffix(mod.Name, "-retract") {
		severity = Warning
	}
	return []Finding{{
		Severity: severity,
		Line:     mod.Line,
		Column:   mod.Column,
		RuleID:   "no-resources",
		Message:  "module has no resources",
	}}
}

func init() { register(noResourcesRule{}) }
