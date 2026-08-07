// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"strings"
)

// execTriggerWhenChangedRule catches the exec-shaped twin of the bug
// serviceSubscribesWhenChangedRule fixes for services.
//
// `subscribes:` (and being the target of another resource's `notify:`)
// only supplies a trigger edge — it does not gate execution. The gate is
// `when: "changed"`: the agent skips a resource only when its parsed
// `when:` expression references the `changed` sentinel and no trigger
// fired (agent/src/runner/mod.rs, execute_resource). An exec with a
// trigger wired up but no `when: "changed"` therefore runs on every
// convergence cycle,
// and an unguarded exec reports Action::Created / changed=true every
// time. That inflates ChangedCount on every run, which is what
// server/convergence/ counts toward DriftLevel — one such resource pins
// its configcrate at Diverged forever.
//
// Scoped narrowly to stay quiet. An exec carrying `onlyif:` or `unless:`
// is already idempotent, and one with `report_settled: true` has had its
// every-cycle execution explicitly declared as maintenance by the
// author — neither is flagged. What is left is genuinely incoherent: a
// trigger was declared, yet nothing consumes it. Either the author meant
// the resource to be trigger-gated and omitted `when: "changed"`, or the
// trigger is dead weight. Both deserve a look.
//
// Unlike the service rule this does NOT auto-fix. Injecting
// `when: "changed"` flips an exec from "runs every cycle" to "never runs
// until triggered"; restarting a daemon every cycle is never intended, so
// the service rule can repair silently, but an exec that happens to rely
// on the every-cycle behavior would be silently switched off. Warn and
// let the author choose.
type execTriggerWhenChangedRule struct{}

func (execTriggerWhenChangedRule) id() string { return "exec-trigger-when-changed" }

func (execTriggerWhenChangedRule) apply(mod *configcrate, _ Options) []Finding {
	// Resources named in some other resource's notify: are triggered even
	// though they carry no subscribes: of their own. Only intra-configcrate
	// notify is visible here — a cross-configcrate notify resolves at
	// publish, the same blind spot the rest of the linter has.
	notified := make(map[string]bool)
	for i := range mod.Resources {
		for _, target := range mod.Resources[i].Notify {
			notified[target] = true
		}
	}

	var out []Finding
	for i := range mod.Resources {
		res := &mod.Resources[i]
		if res.Type != "exec" {
			continue
		}
		if strings.Contains(res.When, "changed") {
			continue
		}
		// Already idempotent, or every-cycle execution was declared.
		if attrStr(res.Attrs, "onlyif") != "" || attrStr(res.Attrs, "unless") != "" {
			continue
		}
		if attrStr(res.Attrs, "report_settled") == "true" {
			continue
		}

		var via string
		switch {
		case len(res.Subscribes) > 0:
			via = fmt.Sprintf("subscribes: %v", res.Subscribes)
		case notified[res.Name]:
			via = "another resource's notify:"
		default:
			continue
		}

		out = append(out, Finding{
			Severity: Warning,
			Line:     res.Line,
			Column:   res.Column,
			RuleID:   "exec-trigger-when-changed",
			Message: fmt.Sprintf(
				"resource %q: exec is triggered (%s) but has no when: \"changed\" guard and no onlyif/unless — "+
					"it runs every convergence cycle and reports changed every time, inflating drift. "+
					"Add when: \"changed\" to gate it on the trigger, or drop the trigger if it runs unconditionally by design",
				res.Name, via,
			),
		})
	}
	return out
}

func init() { register(execTriggerWhenChangedRule{}) }
