// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package modlint

import "fmt"

// moduleTopLevelProvidesBundleRule errors when a module YAML declares
// `provides:` or `bundle:` at the top level — the authoring shape
// introduced in 0.66.48 and retired in 0.66.57 in favor of a single
// `compliance:` parent block.
//
// The 0.66.48 fold-down retired the per-module framework→control list
// shape (handled by rule_configcrate_compliance_key_retired) but parked
// the replacement keys at top level, which stripped the at-a-glance
// "this is a compliance attribution claim" signal from module YAML.
// 0.66.57 puts them back under `compliance:` to restore that signal
// without re-introducing the per-module cross-walk burden.
type moduleTopLevelProvidesBundleRule struct{}

func (moduleTopLevelProvidesBundleRule) id() string { return "configcrate-top-level-provides-bundle" }

func (moduleTopLevelProvidesBundleRule) apply(mod *configcrate, _ Options) []Finding {
	if mod.TopLevelProvidesLine == 0 && mod.TopLevelBundleLine == 0 {
		return nil
	}
	var key string
	var line int
	switch {
	case mod.TopLevelProvidesLine > 0 && mod.TopLevelBundleLine > 0:
		key = "`provides:` and `bundle:`"
		line = mod.TopLevelProvidesLine
		if mod.TopLevelBundleLine < line {
			line = mod.TopLevelBundleLine
		}
	case mod.TopLevelProvidesLine > 0:
		key = "`provides:`"
		line = mod.TopLevelProvidesLine
	default:
		key = "`bundle:`"
		line = mod.TopLevelBundleLine
	}
	return []Finding{{
		Severity: Error,
		Line:     line,
		Column:   1,
		RuleID:   "configcrate-top-level-provides-bundle",
		Message: fmt.Sprintf(
			"top-level %s is retired (0.66.57) — fold under a `compliance:` parent block "+
				"(e.g. `compliance: { provides: [<tag>], bundle: [<id>] }`). "+
				"Restores the at-a-glance compliance signal in configcrate YAML without changing the catalog-backed semantics.",
			key,
		),
	}}
}

func init() { register(moduleTopLevelProvidesBundleRule{}) }
