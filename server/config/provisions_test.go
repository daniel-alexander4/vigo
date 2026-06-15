// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"path/filepath"
	"sort"
	"testing"
)

// TestBuildComplianceIndex_ProvidesUnion pins the loader's union pass:
// a module declaring `compliance.provides: [time-sync]` produces the
// expected per-framework owner list from catalog expansion alone.
// Module migrations rely on this byte-for-byte equivalence.
func TestBuildComplianceIndex_ProvidesUnion(t *testing.T) {
	defs := map[string]*ConfigcrateDefinition{
		"chrony": {Name: "chrony", Compliance: ComplianceClaims{Provides: []string{"time-sync"}}},
	}
	idx := buildComplianceIndex(defs)

	// time-sync expands into pci-dss 10.6.1/2/3 + nist AU-8 + cis 2.1.1.1/2 + iso A.12.4.4 — every owner is "chrony".
	for _, want := range []struct{ framework, control string }{
		{"pci-dss", "10.6.1"},
		{"nist-800-53", "AU-8"},
		{"iso-27001", "A.12.4.4"},
		{"cis-ubuntu", "2.1.1.1"},
		{"cis-rhel", "2.1.1.2"},
	} {
		owners := idx[want.framework][want.control]
		if len(owners) != 1 || owners[0] != "chrony" {
			t.Errorf("idx[%q][%q] = %v, want [chrony]", want.framework, want.control, owners)
		}
	}
}

// TestBuildComplianceIndex_DedupesAcrossSources catches the
// pre-existing duplicate-append bug the rewrite quietly fixed: a module
// declaring the same (framework, control) via both `compliance.provides`
// and a directory-level compliance.vgo inheritance file must appear
// exactly once in the per-control owner list. Without dedup the coverage
// drilldown would credit the module twice.
func TestBuildComplianceIndex_DedupesAcrossSources(t *testing.T) {
	defs := map[string]*ConfigcrateDefinition{
		// time-sync expands pci-dss [10.6.1] (among others);
		// duplicated via the inherited claim that ancestor
		// compliance.vgo files merge in.
		"chrony": {
			Name:       "chrony",
			Compliance: ComplianceClaims{Provides: []string{"time-sync"}},
			InheritedCompliance: map[string][]string{
				"pci-dss": {"10.6.1"},
			},
		},
	}
	idx := buildComplianceIndex(defs)
	owners := idx["pci-dss"]["10.6.1"]
	if len(owners) != 1 || owners[0] != "chrony" {
		t.Errorf("expected chrony once, got %v", owners)
	}
}

// TestBuildComplianceIndex_UnknownProvisionNoop catches the
// fallback: an unknown provision tag contributes nothing (and never
// panics). modlint surfaces typos at module load; the loader stays
// quiet so a slightly-stale catalog doesn't break the world.
func TestBuildComplianceIndex_UnknownProvisionNoop(t *testing.T) {
	defs := map[string]*ConfigcrateDefinition{
		"bogus": {Name: "bogus", Compliance: ComplianceClaims{Provides: []string{"not-a-real-provision-xyz"}}},
	}
	idx := buildComplianceIndex(defs)
	if len(idx) != 0 {
		t.Errorf("unknown provision must produce no index entries, got %v", idx)
	}
}

// TestBuildComplianceIndex_TwoConfigcratesSameProvision pins the
// "no-drift" property the catalog model exists to enforce: two
// modules that both claim the same provision both appear in the
// per-control owner list — the original "chrony vs ntp" drift cause
// is gone by construction.
func TestBuildComplianceIndex_TwoConfigcratesSameProvision(t *testing.T) {
	defs := map[string]*ConfigcrateDefinition{
		"chrony": {Name: "chrony", Compliance: ComplianceClaims{Provides: []string{"time-sync"}}},
		"ntp":    {Name: "ntp", Compliance: ComplianceClaims{Provides: []string{"time-sync"}}},
	}
	idx := buildComplianceIndex(defs)
	owners := append([]string(nil), idx["nist-800-53"]["AU-8"]...)
	sort.Strings(owners)
	if len(owners) != 2 || owners[0] != "chrony" || owners[1] != "ntp" {
		t.Errorf("expected [chrony ntp], got %v", owners)
	}
}

// TestTrace_ProvidesAttribution pins the per-control source
// attribution emitted by ConfigStore.Trace: every control derived from
// a `provides:` tag carries `provides:<id>`; controls declared via
// `compliance:` carry `explicit`; a control declared by both wins
// "explicit" (the operator's hand-written intent is the load-bearing
// signal). `vigocli config trace` keys on this map to label each row.
func TestTrace_ProvidesAttribution(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "configcrates", "chrony.vgo"), `
name: chrony
compliance:
  provides: [time-sync]
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [chrony]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	trace := store.Trace("any.host")
	if !trace.Matched {
		t.Fatal("expected hostname to match the catch-all hostcrate")
	}

	var found bool
	for _, ct := range trace.ComplianceTags {
		if ct.Configcrate != "chrony" {
			continue
		}
		found = true
		if len(ct.Provides) != 1 || ct.Provides[0] != "time-sync" {
			t.Errorf("expected provides=[time-sync], got %v", ct.Provides)
		}
		if ct.ControlSources == nil {
			t.Fatal("expected ControlSources for a module with provides:")
		}
		if got := ct.ControlSources["nist-800-53"]["AU-8"]; got != "provides:time-sync" {
			t.Errorf("nist-800-53/AU-8 source = %q, want provides:time-sync", got)
		}
		if got := ct.ControlSources["pci-dss"]["10.6.1"]; got != "provides:time-sync" {
			t.Errorf("pci-dss/10.6.1 source = %q, want provides:time-sync", got)
		}
	}
	if !found {
		t.Fatal("expected to find ComplianceTags entry for chrony")
	}
}

// TestTrace_ExplicitOverridesProvides pins the attribution tie-break:
// when a control appears in both `compliance.provides` expansion and
// an inherited claim from a directory-level compliance.vgo, the source
// is reported as "explicit" because the operator hand-wrote it — that's
// the signal a future `vigocli config trace` user wants preserved.
func TestTrace_ExplicitOverridesProvides(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "configcrates", "chrony.vgo"), `
name: chrony
compliance:
  provides: [time-sync]
resources:
  - name: noop
    type: exec
    command: "true"
`)
	// Directory-level compliance.vgo — still the framework→control shape
	// (a different file kind from module YAML).
	writeFile(t, filepath.Join(dir, "configcrates", "compliance.vgo"), `
compliance:
  pci-dss: ["10.6.1"]
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [chrony]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	trace := store.Trace("any.host")
	for _, ct := range trace.ComplianceTags {
		if ct.Configcrate != "chrony" {
			continue
		}
		// pci-dss/10.6.1 was claimed both ways; attribution
		// reports the explicit author.
		if got := ct.ControlSources["pci-dss"]["10.6.1"]; got != "explicit" {
			t.Errorf("pci-dss/10.6.1 source = %q, want explicit (operator wrote it directly)", got)
		}
		// nist-800-53/AU-8 came only from provides; attribution
		// reports the provision.
		if got := ct.ControlSources["nist-800-53"]["AU-8"]; got != "provides:time-sync" {
			t.Errorf("nist-800-53/AU-8 source = %q, want provides:time-sync", got)
		}
	}
}
