// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package modlint

import "strings"

// noResourcesRule rejects a module that declares no resources. A module
// without resources is almost always a half-finished file — the operator
// pasted the header and walked away — and applying it to the fleet is a
// no-op that wastes a publish cycle.
//
// Exceptions (rule warns instead of errors):
//
//   - Retract templates (name ends with "-retract") may legitimately have
//     no resources when there's nothing the agent can safely auto-undo
//     (e.g. CIS Windows security policy values, network-device SSH
//     configs, audit settings without a known prior value). The retract
//     is a documentation surface.
//
//   - Windows-companion stubs (name ends with "-windows") may be empty
//     when the Linux module has no native Windows analog (e.g. borg, zfs,
//     opendkim). The stub exists so composite roles can dispatch via
//     `case:` without erroring on Windows envoys; the file's comment
//     points operators at the functional alternative (restic-windows,
//     Storage-Spaces-based equivalent, Exchange's built-in DKIM, etc.).
//
// In all cases the rule still warns so a *half-finished* module is
// visible in `vigocli config trace`, but it doesn't block publish.
type noResourcesRule struct{}

func (noResourcesRule) id() string { return "no-resources" }

func (noResourcesRule) apply(mod *configcrate, _ Options) []Finding {
	if len(mod.Resources) > 0 {
		return nil
	}
	severity := Error
	if strings.HasSuffix(mod.Name, "-retract") || strings.HasSuffix(mod.Name, "-windows") {
		severity = Warning
	}
	return []Finding{{
		Severity: severity,
		Line:     mod.Line,
		Column:   mod.Column,
		RuleID:   "no-resources",
		Message:  "configcrate has no resources",
	}}
}

func init() { register(noResourcesRule{}) }
