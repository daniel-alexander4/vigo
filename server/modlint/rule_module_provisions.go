// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"sort"
	"strings"

	"git.alexander4.io/vigo/server/compliance"
)

// moduleProvisionsRule validates a module's `provides:` list against
// the Vigo-curated catalog at server/compliance/provisions.go, and
// flags redundant declarations where a module also lists the same
// (framework, control) pair in its `compliance:` block.
//
// Unknown provision tag → Error with did-you-mean. The agent's
// compliance index treats an unknown tag as zero contribution
// (resolves to nil in compliance.ResolveProvision), so an unchecked
// typo silently de-tags the module from a framework. Error severity
// catches that at publish time rather than at audit time.
//
// Redundant declaration → Warning. The block isn't broken — explicit
// claims remain valid — but the noise hides the operator's actual
// intent. A warning surfaces it without blocking publish.
//
// Importing compliance keeps the catalog in one place: the
// modlint-rule and the loader resolve the same set, by construction.
// compliance is a data-only package (no server-runtime deps), so the
// inline-list discipline that applies to server/api stays intact.
type moduleProvisionsRule struct{}

func (moduleProvisionsRule) id() string { return "module-provisions" }

func (moduleProvisionsRule) apply(mod *module, _ Options) []Finding {
	if len(mod.Provides) == 0 {
		return nil
	}
	var out []Finding
	known := compliance.KnownProvisionIDs()
	knownSet := make(map[string]bool, len(known))
	for _, id := range known {
		knownSet[id] = true
	}

	// Validate each provision tag against the catalog.
	for _, prov := range mod.Provides {
		if knownSet[prov] {
			continue
		}
		if guess := closestKey(prov, known); guess != "" {
			out = append(out, Finding{
				Severity: Error,
				Line:     mod.Line,
				Column:   mod.Column,
				RuleID:   "module-provisions",
				Message: fmt.Sprintf(
					"provision %q is not in the catalog — did you mean %q? Run `vigocli compliance provisions` to list known tags.",
					prov, guess,
				),
			})
		} else {
			out = append(out, Finding{
				Severity: Error,
				Line:     mod.Line,
				Column:   mod.Column,
				RuleID:   "module-provisions",
				Message: fmt.Sprintf(
					"provision %q is not in the catalog. Run `vigocli compliance provisions` to list known tags.",
					prov,
				),
			})
		}
	}

	// Surface (framework, control) pairs covered by both a known
	// provision and the module's explicit `compliance:` block.
	if len(mod.Compliance) > 0 {
		provided := make(map[string]map[string]string) // framework → control → provision_id
		for _, prov := range mod.Provides {
			if !knownSet[prov] {
				continue
			}
			for framework, controls := range compliance.ResolveProvision(prov) {
				if provided[framework] == nil {
					provided[framework] = make(map[string]string)
				}
				for _, cid := range controls {
					provided[framework][cid] = prov
				}
			}
		}
		type dup struct{ framework, control, prov string }
		var dups []dup
		for framework, controls := range mod.Compliance {
			byCID, ok := provided[framework]
			if !ok {
				continue
			}
			for _, cid := range controls {
				if prov, hit := byCID[cid]; hit {
					dups = append(dups, dup{framework, cid, prov})
				}
			}
		}
		sort.Slice(dups, func(i, j int) bool {
			if dups[i].framework != dups[j].framework {
				return dups[i].framework < dups[j].framework
			}
			return dups[i].control < dups[j].control
		})
		for _, d := range dups {
			out = append(out, Finding{
				Severity: Warning,
				Line:     mod.Line,
				Column:   mod.Column,
				RuleID:   "module-provisions",
				Message: fmt.Sprintf(
					"redundant: compliance.%s[%q] is already covered by provides:[%s] — drop it from the explicit block",
					d.framework, d.control, d.prov,
				),
			})
		}
	}

	// Stable order across findings for golden-style tests.
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Compare(out[i].Message, out[j].Message) < 0
	})
	return out
}

func init() { register(moduleProvisionsRule{}) }
