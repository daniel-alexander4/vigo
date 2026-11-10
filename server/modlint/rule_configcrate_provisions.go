// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package modlint

import (
	"fmt"
	"sort"
	"strings"

	"git.alexander4.io/vigo/server/compliance"
)

// configcrateProvisionsRule validates a configcrate's `provides:` list against
// the Vigo-curated catalog at server/compliance/provisions.go.
//
// Unknown provision tag → Error with did-you-mean. The agent's
// compliance index treats an unknown tag as zero contribution
// (resolves to nil in compliance.ResolveProvision), so an unchecked
// typo silently de-tags the configcrate from a framework. Error severity
// catches that at publish time rather than at audit time.
//
// Importing compliance keeps the catalog in one place: the
// modlint-rule and the loader resolve the same set, by construction.
// compliance is a data-only package (no server-runtime deps), so the
// inline-list discipline that applies to server/api stays intact.
type configcrateProvisionsRule struct{}

func (configcrateProvisionsRule) id() string { return "configcrate-provisions" }

func (configcrateProvisionsRule) apply(mod *configcrate, _ Options) []Finding {
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
				RuleID:   "configcrate-provisions",
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
				RuleID:   "configcrate-provisions",
				Message: fmt.Sprintf(
					"provision %q is not in the catalog. Run `vigocli compliance provisions` to list known tags.",
					prov,
				),
			})
		}
	}

	// Stable order across findings for golden-style tests. (The
	// redundancy check that lived here pre-0.66.48 — "compliance:[X]
	// is already covered by provides:[Y]" — was retired with the
	// per-module `compliance:` key itself. moduleComplianceKeyRetiredRule
	// fires the only valid error when the key appears at all.)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Compare(out[i].Message, out[j].Message) < 0
	})
	return out
}

func init() { register(configcrateProvisionsRule{}) }
