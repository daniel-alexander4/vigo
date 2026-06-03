// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Helper: sort a framework→controls map in place so test assertions are
// order-independent. Union results may arrive in any declaration order.
func sortFrameworkClaims(m map[string][]string) {
	for k := range m {
		sort.Strings(m[k])
	}
}

// ============================================================================
// Envoys-tree regression — rename doesn't break anything
// ============================================================================

// Case 1: compliance.vgo at the stockpile root with a waivers: block —
// merged into cs.waivers just like the old waivers.vgo was.
func TestCompliance_EnvoysTreeRoot_WaiversMerge(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [base]
`)
	writeFile(t, filepath.Join(dir, "waivers.vgo"), `
waivers:
  cis-ubuntu:
    - control: "5.4.1"
      reason: "test envoy, short-lived"
      approved_by: "dan@alexander4.org"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	waivers := store.Waivers("cis-ubuntu")
	if len(waivers) != 1 {
		t.Fatalf("expected 1 cis-ubuntu waiver, got %d", len(waivers))
	}
	if waivers["5.4.1"] == nil {
		t.Errorf("expected waiver for control 5.4.1, got nil")
	}
}

// Case 2: compliance.vgo in a deeper envoys-tree directory — waivers
// inherit per the existing chain logic.
func TestCompliance_EnvoysTreeSubdir_WaiversInherit(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [base]
`)
	writeFile(t, filepath.Join(dir, "default", "waivers.vgo"), `
waivers:
  pci-dss:
    - control: "8.1.1"
      reason: "bootstrapping fleet"
      approved_by: "dan"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	if store.Waivers("pci-dss")["8.1.1"] == nil {
		t.Errorf("expected pci-dss 8.1.1 waiver from default/compliance.vgo")
	}
}

// Case 3: scaffolding/waivers.vgo with a compliance: key — rejected
// fatally by the worksite layout validator (phase 3, 0.29.1). Previously
// compliance: was silently ignored with a warning; the rule is now
// enforced. validate_layout_test.go covers the rejection details; this
// test confirms NewConfigStore refuses to load and keeps prior config
// live.
func TestCompliance_EnvoysTree_ComplianceKeyIgnored(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [base]
`)
	writeFile(t, filepath.Join(dir, "waivers.vgo"), `
compliance:
  cis: ["5.4.1"]
waivers:
  cis:
    - control: "5.4.2"
      reason: "ephemeral"
      approved_by: "dan"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected NewConfigStore to reject compliance: in scaffolding/waivers.vgo")
	}
	if !strings.Contains(err.Error(), "compliance: block in waivers.vgo") {
		t.Errorf("error should identify the wrong-key violation; got: %s", err)
	}
}

// ============================================================================
// Configcrates-tree — new capability
// ============================================================================

// Case 4: modules/compliance.vgo with fleet-wide claims — every module
// under modules/ inherits them.
func TestCompliance_ModulesTreeRoot_ClaimsInheritFleetwide(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "compliance.vgo"), `
compliance:
  cis: ["1.1.1"]
`)
	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "configcrates", "users", "dan.vgo"), `
name: users/dan
resources:
  - name: dan
    type: user
    state: present
    username: dan
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [base, users/dan]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("any.host")
	for _, mod := range cfg.Configcrates {
		claims, ok := mod.Compliance["cis"]
		if !ok || !reflect.DeepEqual(claims, []string{"1.1.1"}) {
			t.Errorf("module %s: cis claims = %v, want [1.1.1]", mod.Name, claims)
		}
	}
}

// Case 5: modules/users/compliance.vgo in a subdirectory with
// user-management-specific claims — only modules under modules/users/
// inherit them.
func TestCompliance_ModulesTreeSubdir_ClaimsScopedToSubtree(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "configcrates", "users", "compliance.vgo"), `
compliance:
  cis-ubuntu: ["5.4.1"]
`)
	writeFile(t, filepath.Join(dir, "configcrates", "users", "dan.vgo"), `
name: users/dan
resources:
  - name: dan
    type: user
    state: present
    username: dan
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [base, users/dan]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("any.host")
	var baseFound, danFound bool
	for _, mod := range cfg.Configcrates {
		switch mod.Name {
		case "base":
			baseFound = true
			if _, ok := mod.Compliance["cis-ubuntu"]; ok {
				t.Errorf("base module should NOT inherit cis-ubuntu from modules/users/")
			}
		case "users/dan":
			danFound = true
			if !reflect.DeepEqual(mod.Compliance["cis-ubuntu"], []string{"5.4.1"}) {
				t.Errorf("users/dan: cis-ubuntu = %v, want [5.4.1]", mod.Compliance["cis-ubuntu"])
			}
		}
	}
	if !baseFound || !danFound {
		t.Errorf("expected both base and users/dan in resolved config")
	}
}

// Case 6: module-specific compliance.vgo (sibling to the module file)
// + ancestor compliance.vgo claims — effective claims are the union.
// Post-0.66.57 the per-module framework→control attribution comes
// through a sibling compliance.vgo file; the module YAML itself
// carries only catalog-tag claims via `compliance: { provides, bundle }`.
func TestCompliance_ModulesTree_UnionWithModuleOwnBlock(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "compliance.vgo"), `
compliance:
  cis: ["1.1.1"]
`)
	writeFile(t, filepath.Join(dir, "configcrates", "users", "compliance.vgo"), `
compliance:
  cis: ["1.1.2"]
  nist: ["AC-3"]
`)
	writeFile(t, filepath.Join(dir, "configcrates", "users", "dan", "compliance.vgo"), `
compliance:
  cis: ["1.1.3"]
  soc2: ["CC6.1"]
`)
	writeFile(t, filepath.Join(dir, "configcrates", "users", "dan", "dan.vgo"), `
name: users/dan
resources:
  - name: dan
    type: user
    state: present
    username: dan
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [users/dan]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("any.host")
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("expected 1 module, got %d", len(cfg.Configcrates))
	}
	got := cfg.Configcrates[0].Compliance
	sortFrameworkClaims(got)

	want := map[string][]string{
		"cis":  {"1.1.1", "1.1.2", "1.1.3"},
		"nist": {"AC-3"},
		"soc2": {"CC6.1"},
	}
	for fw, controls := range want {
		sort.Strings(controls)
		if !reflect.DeepEqual(got[fw], controls) {
			t.Errorf("%s: got %v, want %v", fw, got[fw], controls)
		}
	}
}

// Case 7: modules/compliance.vgo with a waivers: block — waivers ignored,
// no effect — rejected fatally by the worksite layout validator (phase 3,
// 0.29.1). Previously waivers: was silently ignored with a warning;
// now it blocks load. validate_layout_test.go covers detailed error
// shape; this test confirms the legacy NewConfigStore path refuses.
func TestCompliance_ModulesTree_WaiversIgnored(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "compliance.vgo"), `
compliance:
  cis: ["1.1.1"]
waivers:
  cis:
    - control: "1.1.1"
      reason: "should not take effect"
      approved_by: "dan"
`)
	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [base]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected NewConfigStore to reject waivers: in stockpile/modules/compliance.vgo")
	}
	if !strings.Contains(err.Error(), "waivers: block in compliance.vgo") {
		t.Errorf("error should identify the wrong-key violation; got: %s", err)
	}
}

// ============================================================================
// Error/validation tests
// ============================================================================

// Case 8: compliance.vgo with an unknown top-level key → parse error.
func TestCompliance_UnknownTopLevelKey_Rejected(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [base]
`)
	writeFile(t, filepath.Join(dir, "waivers.vgo"), `
vars:
  foo: bar
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for unknown top-level key, got nil")
	}
	if !strings.Contains(err.Error(), "unknown top-level key") {
		t.Errorf("error message should mention 'unknown top-level key', got: %v", err)
	}
}

// (Removed) Case 9 — "legacy waivers.vgo file → skipped". The test relied
// on dropping a waivers.vgo *outside* the stockpile content root, exercising
// nothing more than "files outside the walked tree are ignored." After the
// 0.65 layout flattening, the test's "outside" path lands *inside* the
// content root, where the file is a legitimate top-level waivers.vgo.
// loadStockpileWaivers explicitly supports top-level waivers per its
// canonical-filename rule, so there is no longer a behavior here worth
// asserting.

// Case 10: Multi-framework union across two directory levels.
func TestCompliance_MultiFramework_UnionAcrossLevels(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
`)
	writeFile(t, filepath.Join(dir, "configcrates", "users", "compliance.vgo"), `
compliance:
  nist-800-53: ["AC-3", "AC-6"]
`)
	writeFile(t, filepath.Join(dir, "configcrates", "users", "dan.vgo"), `
name: users/dan
resources:
  - name: dan
    type: user
    state: present
    username: dan
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [users/dan]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	got := store.Get("any.host").Configcrates[0].Compliance
	sortFrameworkClaims(got)

	wantCis := []string{"5.4.1"}
	wantNist := []string{"AC-3", "AC-6"}
	if !reflect.DeepEqual(got["cis"], wantCis) {
		t.Errorf("cis = %v, want %v", got["cis"], wantCis)
	}
	if !reflect.DeepEqual(got["nist-800-53"], wantNist) {
		t.Errorf("nist-800-53 = %v, want %v", got["nist-800-53"], wantNist)
	}
}

// ============================================================================
// Regression — existing waivers behavior via compliance.vgo rename
// ============================================================================

// Case 11: Waiver expiry still works after the rename.
func TestCompliance_WaiverExpiry_StillWorks(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: [base]
`)
	writeFile(t, filepath.Join(dir, "waivers.vgo"), `
waivers:
  cis:
    - control: "5.4.1"
      reason: "expired"
      approved_by: "dan"
      expires: "2020-01-01"
    - control: "5.4.2"
      reason: "active"
      approved_by: "dan"
      expires: "2099-12-31"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	waivers := store.Waivers("cis")
	if waivers["5.4.1"] != nil {
		t.Errorf("expired waiver 5.4.1 should be excluded")
	}
	if waivers["5.4.2"] == nil {
		t.Errorf("non-expired waiver 5.4.2 should be present")
	}
}

// ============================================================================
// Usercrate-tree compliance inheritance — mirrors the modules-tree behavior
// ============================================================================

// Case U1: stockpile/usercrates/compliance.vgo with fleet-wide claims —
// every usercrate under usercrates/ inherits them.
func TestCompliance_UsercratesTreeRoot_ClaimsInheritFleetwide(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "usercrates", "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
  nist-800-53: ["AC-3"]
`)
	writeFile(t, filepath.Join(dir, "usercrates", "dan.vgo"), `
resources:
  - name: dan
    type: user
    state: present
    username: dan
`)
	writeFile(t, filepath.Join(dir, "usercrates", "alice.vgo"), `
name: alice
resources:
  - name: alice
    type: user
    state: present
    username: alice
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    usercrates:
      - dan
      - alice
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("any.host")
	var danFound, aliceFound bool
	for _, mod := range cfg.Configcrates {
		switch mod.Name {
		case "dan":
			danFound = true
			if !reflect.DeepEqual(mod.Compliance["cis"], []string{"5.4.1"}) {
				t.Errorf("dan: cis = %v, want [5.4.1]", mod.Compliance["cis"])
			}
			if !reflect.DeepEqual(mod.Compliance["nist-800-53"], []string{"AC-3"}) {
				t.Errorf("dan: nist-800-53 = %v, want [AC-3]", mod.Compliance["nist-800-53"])
			}
		case "alice":
			aliceFound = true
			if !reflect.DeepEqual(mod.Compliance["cis"], []string{"5.4.1"}) {
				t.Errorf("alice: cis = %v, want [5.4.1]", mod.Compliance["cis"])
			}
		}
	}
	if !danFound || !aliceFound {
		t.Errorf("expected both dan (defaulted name) and alice in resolved config; got modules = %v", moduleNames(cfg.Configcrates))
	}
}

// Case U2: scoped usercrates/compliance.vgo — claims apply only to
// usercrates in that scope, not to root-level usercrates or modules.
func TestCompliance_UsercratesTreeScoped_ClaimsScopedToSubtree(t *testing.T) {
	dir := setupDir(t)

	// Root-level usercrate — should NOT inherit customerA's claims.
	writeFile(t, filepath.Join(dir, "usercrates", "globaladmin.vgo"), `
resources:
  - name: globaladmin
    type: user
    state: present
    username: globaladmin
`)
	// CustomerA-scoped compliance + usercrate (lives under stockpile/).
	writeFile(t, filepath.Join(dir, "customerA", "usercrates", "compliance.vgo"), `
compliance:
  pci-dss: ["8.1.1"]
`)
	writeFile(t, filepath.Join(dir, "customerA", "usercrates", "alice.vgo"), `
resources:
  - name: alice
    type: user
    state: present
    username: alice
`)
	// Hostcrate scoped to customerA envoys.
	writeFile(t, filepath.Join(dir, "customerA", "envoys.vgo"), `
envoys:
  - match: "a01"
    usercrates:
      - alice
      - globaladmin
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "root01"
    usercrates:
      - globaladmin
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// a01 sees customerA's alice with the PCI claim AND globaladmin (no claim).
	cfgA := store.Get("a01")
	var aliceFound, rootAdminInA bool
	for _, mod := range cfgA.Configcrates {
		switch mod.Name {
		case "alice":
			aliceFound = true
			if !reflect.DeepEqual(mod.Compliance["pci-dss"], []string{"8.1.1"}) {
				t.Errorf("alice on a01: pci-dss = %v, want [8.1.1]", mod.Compliance["pci-dss"])
			}
		case "globaladmin":
			rootAdminInA = true
			if _, ok := mod.Compliance["pci-dss"]; ok {
				t.Errorf("globaladmin on a01 must NOT inherit customerA's pci-dss claim")
			}
		}
	}
	if !aliceFound || !rootAdminInA {
		t.Errorf("a01 should have both alice and globaladmin; got %v", moduleNames(cfgA.Configcrates))
	}

	// root01 sees only globaladmin (no claim), NOT alice.
	cfgR := store.Get("root01")
	for _, mod := range cfgR.Configcrates {
		if mod.Name == "alice" {
			t.Errorf("root01 must not see customerA's alice; got modules = %v", moduleNames(cfgR.Configcrates))
		}
		if mod.Name == "globaladmin" {
			if _, ok := mod.Compliance["pci-dss"]; ok {
				t.Errorf("globaladmin on root01: customerA's pci-dss leaked into root scope")
			}
		}
	}
}

// Case U3: waivers: block inside a usercrate-tree compliance.vgo — now
// rejected fatally (phase 3, 0.29.1). stockpile/**/compliance.vgo holds
// claims only; any waivers: entries belong in scaffolding/waivers.vgo.
func TestCompliance_UsercratesTree_WaiversIgnored(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "usercrates", "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
waivers:
  cis:
    - control: "9.9.9"
      reason: "this should be ignored"
      approved_by: "dan"
`)
	writeFile(t, filepath.Join(dir, "usercrates", "dan.vgo"), `
resources:
  - name: dan
    type: user
    state: present
    username: dan
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected NewConfigStore to reject waivers: in stockpile/usercrates/compliance.vgo")
	}
	if !strings.Contains(err.Error(), "waivers: block in compliance.vgo") {
		t.Errorf("error should identify the wrong-key violation; got: %s", err)
	}
}

// Case U4: compliance.vgo inside a usercrates/ dir is NOT parsed as a
// usercrate definition — regression against the error that surfaced when
// a user dropped compliance.vgo into usercrates/ before 0.27.3.
func TestCompliance_UsercratesTree_ComplianceFileNotTreatedAsUsercrate(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "usercrates", "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
`)
	// No other usercrates, no envoys — just a bare compliance.vgo file.
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore should not fail on bare usercrates/compliance.vgo: %v", err)
	}

	// No usercrates registered — compliance.vgo must not count.
	cfg := store.Get("any.host")
	for _, mod := range cfg.Configcrates {
		if mod.Name == "compliance" {
			t.Errorf("compliance.vgo was parsed as a usercrate named 'compliance'; modules = %v", moduleNames(cfg.Configcrates))
		}
	}
}

// Case U5: the config-trace "Compliance Sources" list shows each
// compliance.vgo once. Regression for the duplicate-card bug — a
// usercrates/compliance.vgo was collected by loadModuleComplianceConfigs and
// a redundant loadUsercrateComplianceConfigs, whose source slices were
// concatenated without dedup, producing two identical trace entries.
func TestCompliance_TraceListsEachComplianceSourceOnce(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "usercrates", "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
`)
	writeFile(t, filepath.Join(dir, "usercrates", "dan.vgo"), `
resources:
  - name: dan
    type: user
    state: present
    username: dan
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    usercrates:
      - dan
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("any.host")
	count := 0
	for _, mcf := range trace.ConfigcrateComplianceFiles {
		if mcf.SourceFile == "usercrates/compliance.vgo" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("usercrates/compliance.vgo appears %d times in trace.ConfigcrateComplianceFiles, want exactly 1", count)
	}
}
