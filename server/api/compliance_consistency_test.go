// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"testing"

	"git.alexander4.io/vigo/server/compliance"
	"git.alexander4.io/vigo/server/config"
)

// cisStandardKeys are excluded from the builder-subset check below.
// All CIS frameworks now migrated to their adopted profiles and are
// enforced: cis-windows (Server 2022 L1-Member-Server), cis-ubuntu and
// cis-rhel (ComplianceAsCode v2.0.0 L1-Server). The map is intentionally
// empty — re-add a key only to temporarily exclude a framework mid-migration.
var cisStandardKeys = map[string]bool{}

// TestEveryStandardSlugHasABuilder guards the framework registries against
// drift: every URL slug in config.StandardKeyForSlug must resolve to a
// frameworkDef with a control-catalog builder. A slug added to
// StandardKeyForSlug without a matching frameworkDef entry (the classic
// failure mode when splitting/adding a framework across the ~10 hand-kept
// registries) fails here instead of 404-ing at runtime.
func TestEveryStandardSlugHasABuilder(t *testing.T) {
	h := &ReportHandler{}
	for slug := range config.StandardKeyForSlug {
		if _, ok := h.frameworkDef(AuditChainStatus{}, slug); !ok {
			t.Errorf("slug %q is in config.StandardKeyForSlug but has no frameworkDef builder", slug)
		}
	}
}

// TestBundleProvisionControlsExistInBuilder enforces the core catalog
// invariant: every control ID a configcrate can claim (via a bundle or
// provision Frameworks map) must exist in that framework's displayed
// builder catalog. A claimed ID absent from the builder silently vanishes
// from coverage (the display universe is the builder, never the claim
// index), so this catches both renumbering drift and typos. CIS is
// excluded per cisStandardKeys.
func TestBundleProvisionControlsExistInBuilder(t *testing.T) {
	h := &ReportHandler{}

	// builderIDs: standard key -> set of control IDs in the displayed catalog.
	builderIDs := make(map[string]map[string]bool)
	for slug, key := range config.StandardKeyForSlug {
		fw, ok := h.frameworkDef(AuditChainStatus{}, slug)
		if !ok {
			continue // reported by TestEveryStandardSlugHasABuilder
		}
		set := make(map[string]bool)
		for _, c := range fw.controls() {
			set[c.ControlID] = true
		}
		builderIDs[key] = set
	}

	check := func(src string, frameworks map[string][]string) {
		for key, ids := range frameworks {
			if cisStandardKeys[key] {
				continue
			}
			set, ok := builderIDs[key]
			if !ok {
				t.Errorf("%s references framework %q with no builder catalog", src, key)
				continue
			}
			for _, id := range ids {
				if !set[id] {
					t.Errorf("%s claims %s control %q, which is not in the builder catalog (would vanish from coverage)", src, key, id)
				}
			}
		}
	}

	for id, b := range compliance.ConfigcrateBundles {
		check("bundle "+id, b.Frameworks)
	}
	for id, p := range compliance.ConfigcrateProvisions {
		check("provision "+id, p.Frameworks)
	}
}
