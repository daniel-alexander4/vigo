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

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "compliance.vgo"), `
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

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "default", "compliance.vgo"), `
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

// Case 3: compliance.vgo with both compliance: and waivers: in an
// envoys-tree location — only waivers: takes effect, compliance: ignored
// (with a warning), no error.
func TestCompliance_EnvoysTree_ComplianceKeyIgnored(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
waivers:
  cis:
    - control: "5.4.2"
      reason: "ephemeral"
      approved_by: "dan"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// Waiver is active.
	if store.Waivers("cis")["5.4.2"] == nil {
		t.Errorf("expected cis 5.4.2 waiver to be active")
	}
	// The compliance: block should NOT have leaked into the base module.
	base := store.Get("any.host").Modules[0]
	if claims, ok := base.Compliance["cis"]; ok {
		t.Errorf("envoys-tree compliance: leaked into module: %v", claims)
	}
}

// ============================================================================
// Modules-tree — new capability
// ============================================================================

// Case 4: modules/compliance.vgo with fleet-wide claims — every module
// under modules/ inherits them.
func TestCompliance_ModulesTreeRoot_ClaimsInheritFleetwide(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "compliance.vgo"), `
compliance:
  cis: ["1.1.1"]
`)
	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "modules", "users", "dan.vgo"), `
name: users/dan
resources:
  - name: dan
    type: user
    username: dan
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
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

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "modules", "users", "compliance.vgo"), `
compliance:
  cis-ubuntu: ["5.4.1"]
`)
	writeFile(t, filepath.Join(dir, "modules", "users", "dan.vgo"), `
name: users/dan
resources:
  - name: dan
    type: user
    username: dan
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
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

	writeFile(t, filepath.Join(dir, "modules", "compliance.vgo"), `
compliance:
  cis: ["1.1.1"]
`)
	writeFile(t, filepath.Join(dir, "modules", "users", "compliance.vgo"), `
compliance:
  cis: ["1.1.2"]
  nist: ["AC-3"]
`)
	writeFile(t, filepath.Join(dir, "modules", "users", "dan.vgo"), `
name: users/dan
compliance:
  cis: ["1.1.3"]
  soc2: ["CC6.1"]
resources:
  - name: dan
    type: user
    username: dan
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
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
// no effect, logged warning.
func TestCompliance_ModulesTree_WaiversIgnored(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "compliance.vgo"), `
compliance:
  cis: ["1.1.1"]
waivers:
  cis:
    - control: "1.1.1"
      reason: "should not take effect"
      approved_by: "dan"
`)
	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// Waiver should NOT be active — it was in a modules-tree file.
	if waiver := store.Waivers("cis")["1.1.1"]; waiver != nil {
		t.Errorf("waiver in modules-tree compliance.vgo should be ignored, got %+v", waiver)
	}
	// But the compliance: block IS still active.
	cfg := store.Get("any.host")
	if !reflect.DeepEqual(cfg.Modules[0].Compliance["cis"], []string{"1.1.1"}) {
		t.Errorf("base module: cis = %v, want [1.1.1]", cfg.Modules[0].Compliance["cis"])
	}
}

// ============================================================================
// Error/validation tests
// ============================================================================

// Case 8: compliance.vgo with an unknown top-level key → parse error.
func TestCompliance_UnknownTopLevelKey_Rejected(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "compliance.vgo"), `
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

// Case 9: legacy waivers.vgo file → rejected with rename hint.
func TestCompliance_LegacyWaiversFile_Rejected(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "waivers.vgo"), `
waivers:
  cis: []
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for legacy waivers.vgo file, got nil")
	}
	if !strings.Contains(err.Error(), "renamed to compliance.vgo") {
		t.Errorf("error should mention rename, got: %v", err)
	}
}

// Case 10: Multi-framework union across two directory levels.
func TestCompliance_MultiFramework_UnionAcrossLevels(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "compliance.vgo"), `
compliance:
  cis: ["5.4.1"]
`)
	writeFile(t, filepath.Join(dir, "modules", "users", "compliance.vgo"), `
compliance:
  nist-800-53: ["AC-3", "AC-6"]
`)
	writeFile(t, filepath.Join(dir, "modules", "users", "dan.vgo"), `
name: users/dan
resources:
  - name: dan
    type: user
    username: dan
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
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

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*"
    modules: [base]
`)
	writeFile(t, filepath.Join(dir, "compliance.vgo"), `
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
