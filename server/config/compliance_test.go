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

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "waivers.vgo"), `
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

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "waivers.vgo"), `
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

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "waivers.vgo"), `
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
// Modules-tree — new capability
// ============================================================================

// Case 4: modules/compliance.vgo with fleet-wide claims — every module
// under modules/ inherits them.
func TestCompliance_ModulesTreeRoot_ClaimsInheritFleetwide(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "compliance.vgo"), `
compliance:
  cis: ["1.1.1"]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "users", "dan.vgo"), `
name: users/dan
resources:
  - name: dan
    type: user
    username: dan
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base, users/dan]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("any.host")
	for _, mod := range cfg.Modules {
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

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "users", "compliance.vgo"), `
compliance:
  cis-ubuntu: ["5.4.1"]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "users", "dan.vgo"), `
name: users/dan
resources:
  - name: dan
    type: user
    username: dan
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base, users/dan]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("any.host")
	var baseFound, danFound bool
	for _, mod := range cfg.Modules {
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

// Case 6: module with its own compliance: block + ancestor compliance.vgo
// claims — effective claims are the union.
func TestCompliance_ModulesTree_UnionWithModuleOwnBlock(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "compliance.vgo"), `
compliance:
  cis: ["1.1.1"]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "users", "compliance.vgo"), `
compliance:
  cis: ["1.1.2"]
  nist: ["AC-3"]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "users", "dan.vgo"), `
name: users/dan
compliance:
  cis: ["1.1.3"]
  soc2: ["CC6.1"]
resources:
  - name: dan
    type: user
    username: dan
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [users/dan]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("any.host")
	if len(cfg.Modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(cfg.Modules))
	}
	got := cfg.Modules[0].Compliance
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

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "compliance.vgo"), `
compliance:
  cis: ["1.1.1"]
waivers:
  cis:
    - control: "1.1.1"
      reason: "should not take effect"
      approved_by: "dan"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
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

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "waivers.vgo"), `
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

// Case 9: legacy waivers.vgo file → skipped with a loud slog.Error, rest
// of the config still loads. Making the rejection fatal cascaded through
// newConfigStore and left main.go in a broken empty-config-store state
// (see commit history around 0.21.3 → 0.21.4). The rename hint now fires
// as a log line and the file is ignored.
func TestCompliance_LegacyWaiversFile_Skipped(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	// Drop a legacy waivers.vgo file — it should be skipped without
	// blocking the rest of the config from loading.
	writeFile(t, filepath.Join(dir, "waivers.vgo"), `
waivers:
  cis:
    - control: "5.4.1"
      reason: "ignored; file is legacy"
      approved_by: "dan"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore should not error on legacy waivers.vgo, got: %v", err)
	}

	// The waiver from the legacy file should NOT be active — the file was skipped.
	if waiver := store.Waivers("cis")["5.4.1"]; waiver != nil {
		t.Errorf("legacy waivers.vgo should have been skipped, got waiver: %+v", waiver)
	}

	// The main config should still have loaded: the base module resolves and
	// the envoys entry is matchable.
	cfg := store.Get("any.host")
	if len(cfg.Modules) != 1 || cfg.Modules[0].Name != "base" {
		t.Errorf("base module should have loaded despite legacy waivers.vgo, got modules: %v", cfg.Modules)
	}
}

// Case 10: Multi-framework union across two directory levels.
func TestCompliance_MultiFramework_UnionAcrossLevels(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "users", "compliance.vgo"), `
compliance:
  nist-800-53: ["AC-3", "AC-6"]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "users", "dan.vgo"), `
name: users/dan
resources:
  - name: dan
    type: user
    username: dan
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [users/dan]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	got := store.Get("any.host").Modules[0].Compliance
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

	writeFile(t, filepath.Join(dir, "stockpile", "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "waivers.vgo"), `
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

	writeFile(t, filepath.Join(dir, "stockpile", "usercrates", "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
  nist-800-53: ["AC-3"]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "usercrates", "dan.vgo"), `
resources:
  - name: dan
    type: user
    username: dan
`)
	writeFile(t, filepath.Join(dir, "stockpile", "usercrates", "alice.vgo"), `
name: alice
resources:
  - name: alice
    type: user
    username: alice
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
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
	for _, mod := range cfg.Modules {
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
		t.Errorf("expected both dan (defaulted name) and alice in resolved config; got modules = %v", moduleNames(cfg.Modules))
	}
}

// Case U2: scoped usercrates/compliance.vgo — claims apply only to
// usercrates in that scope, not to root-level usercrates or modules.
func TestCompliance_UsercratesTreeScoped_ClaimsScopedToSubtree(t *testing.T) {
	dir := setupDir(t)

	// Root-level usercrate — should NOT inherit customerA's claims.
	writeFile(t, filepath.Join(dir, "stockpile", "usercrates", "globaladmin.vgo"), `
resources:
  - name: globaladmin
    type: user
    username: globaladmin
`)
	// CustomerA-scoped compliance + usercrate (lives under stockpile/).
	writeFile(t, filepath.Join(dir, "stockpile", "customerA", "usercrates", "compliance.vgo"), `
compliance:
  pci-dss: ["8.1.1"]
`)
	writeFile(t, filepath.Join(dir, "stockpile", "customerA", "usercrates", "alice.vgo"), `
resources:
  - name: alice
    type: user
    username: alice
`)
	// Matchfile scoped to customerA envoys.
	writeFile(t, filepath.Join(dir, "stockpile", "customerA", "envoys.vgo"), `
envoys:
  - match: "a01"
    usercrates:
      - alice
      - globaladmin
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
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
	for _, mod := range cfgA.Modules {
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
		t.Errorf("a01 should have both alice and globaladmin; got %v", moduleNames(cfgA.Modules))
	}

	// root01 sees only globaladmin (no claim), NOT alice.
	cfgR := store.Get("root01")
	for _, mod := range cfgR.Modules {
		if mod.Name == "alice" {
			t.Errorf("root01 must not see customerA's alice; got modules = %v", moduleNames(cfgR.Modules))
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

	writeFile(t, filepath.Join(dir, "stockpile", "usercrates", "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
waivers:
  cis:
    - control: "9.9.9"
      reason: "this should be ignored"
      approved_by: "dan"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "usercrates", "dan.vgo"), `
resources:
  - name: dan
    type: user
    username: dan
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
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

	writeFile(t, filepath.Join(dir, "stockpile", "usercrates", "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
`)
	// No other usercrates, no envoys — just a bare compliance.vgo file.
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "*"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore should not fail on bare usercrates/compliance.vgo: %v", err)
	}

	// No usercrates registered — compliance.vgo must not count.
	cfg := store.Get("any.host")
	for _, mod := range cfg.Modules {
		if mod.Name == "compliance" {
			t.Errorf("compliance.vgo was parsed as a usercrate named 'compliance'; modules = %v", moduleNames(cfg.Modules))
		}
	}
}
