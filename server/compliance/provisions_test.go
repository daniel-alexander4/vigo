// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package compliance

import (
	"testing"
)

// TestResolveProvision_Known pins the contract every other layer keys
// on: a known provision returns its catalog framework map; an unknown
// one returns nil (so the loader's union pass adds nothing — quiet
// no-op rather than panic — while modlint catches the typo at module
// load).
func TestResolveProvision_Known(t *testing.T) {
	fw := ResolveProvision("time-sync")
	if fw == nil {
		t.Fatal("expected time-sync to be in the catalog")
	}
	if _, ok := fw["pci-dss"]; !ok {
		t.Errorf("time-sync should claim pci-dss controls, got %v", fw)
	}
	if _, ok := fw["nist-800-53"]; !ok {
		t.Errorf("time-sync should claim nist-800-53 controls, got %v", fw)
	}
}

func TestResolveProvision_Unknown(t *testing.T) {
	if got := ResolveProvision("not-a-real-provision-xyz"); got != nil {
		t.Errorf("unknown provision must return nil, got %v", got)
	}
}

// TestKnownProvisionIDs_Sorted pins the sort order modlint's
// did-you-mean iterator and `vigocli compliance provisions` rely on.
func TestKnownProvisionIDs_Sorted(t *testing.T) {
	ids := KnownProvisionIDs()
	if len(ids) == 0 {
		t.Fatal("expected at least one provision in catalog")
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Errorf("KnownProvisionIDs not sorted: %q >= %q at %d", ids[i-1], ids[i], i)
		}
	}
}

// TestCatalogConsistency walks every provision and checks every
// framework slug appears in the canonical set used elsewhere. A typo
// here (e.g. "nist" instead of "nist-800-53") would silently de-tag the
// provision from coverage at runtime — this test catches that at
// build/test time before the catalog ships.
func TestCatalogConsistency(t *testing.T) {
	// Mirror of canonicalFrameworkSlugs in
	// server/modlint/rule_compliance_framework.go — kept inline so
	// this test doesn't import modlint (modlint is the lint layer,
	// compliance is the data layer, and the inverse import would
	// muddy the layering).
	knownFrameworks := map[string]bool{
		"ccss": true, "cis-rhel": true,
		"cis-ubuntu": true, "cis-windows": true, "cyber-essentials": true,
		"fda-21cfr11": true, "finra": true, "gaming": true,
		"gdpr": true, "hipaa": true, "hitrust": true,
		"iec-62443": true, "iso-27001": true, "mifid2": true,
		"nerc-cip": true, "nist-800-53": true, "ny-dfs-cyber": true,
		"pci-dss": true, "soc2": true, "sox": true,
	}
	for id, prov := range ConfigcrateProvisions {
		if prov.ID != id {
			t.Errorf("provision map key %q != entry ID %q", id, prov.ID)
		}
		if prov.Title == "" {
			t.Errorf("provision %q has no title", id)
		}
		if prov.Description == "" {
			t.Errorf("provision %q has no description", id)
		}
		if len(prov.Frameworks) == 0 {
			t.Errorf("provision %q claims no frameworks", id)
		}
		for fw := range prov.Frameworks {
			if !knownFrameworks[fw] {
				t.Errorf("provision %q references unknown framework %q", id, fw)
			}
		}
	}
}
