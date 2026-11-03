// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import (
	"fmt"
	"strings"
)

// serviceSubscribesWhenChangedRule auto-fixes a common idempotency bug.
//
// A service resource with `state: restarted` (or `state: reloaded`)
// plus a `subscribes:` list, but no `when: "changed"` guard, will fire
// the restart on every check-in cycle — not just when the dependency
// actually changes. The fix is mechanical: inject `when: "changed"`.
// Without it, daemons get bounced every check-in interval and the host
// is permanently noisy.
//
// This is one of the few rules that mutates the module. The Finding is
// emitted with FixApplied=true so a JSON-consuming caller can render
// it differently from a "you must act" finding.
type serviceSubscribesWhenChangedRule struct{}

func (serviceSubscribesWhenChangedRule) id() string { return "service-subscribes-when-changed" }

func (serviceSubscribesWhenChangedRule) apply(mod *configcrate, _ Options) []Finding {
	var out []Finding
	for i := range mod.Resources {
		res := &mod.Resources[i]
		if res.Type != "service" {
			continue
		}
		state := attrStr(res.Attrs, "state")
		if state != "restarted" && state != "reloaded" {
			continue
		}
		if len(res.Subscribes) == 0 || strings.Contains(res.When, "changed") {
			continue
		}
		res.When = "changed"
		out = append(out, Finding{
			Severity:   Warning,
			Line:       res.Line,
			Column:     res.Column,
			RuleID:     "service-subscribes-when-changed",
			FixApplied: true,
			Message: fmt.Sprintf(
				"resource %q: added when: \"changed\" — service with state %q and subscribes must be gated to prevent restart every cycle",
				res.Name, state,
			),
		})
	}
	return out
}

func init() { register(serviceSubscribesWhenChangedRule{}) }
