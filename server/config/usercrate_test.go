// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeUsercrateDir writes a usercrate .vgo file under
// <dir>/<scope>/usercrates/<name>.vgo. scope == "" means the root
// stockpile/usercrates/ directory.
func writeUsercrateDir(t *testing.T, dir, scope, name string) {
	t.Helper()
	usercrateDir := filepath.Join(dir, scope, "usercrates")
	if err := os.MkdirAll(usercrateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`
name: %s
resources:
  - name: create-%s
    type: user
    username: %s
`, name, name, name)
	writeFile(t, filepath.Join(usercrateDir, name+".vgo"), content)
}

func TestUsercrate_RootScopeAppliesEverywhere(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "", "admin")

	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("plex")
	if cfg == nil {
		t.Fatal("expected config")
	}
	if !hasModule(cfg.Modules, "admin") {
		t.Errorf("root-level usercrate admin did not apply; modules = %v", moduleNames(cfg.Modules))
	}
}

func TestUsercrate_ScopedOnlyAppliesInBranch(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "", "admin")
	writeUsercrateDir(t, dir, "customerA", "alice")
	writeUsercrateDir(t, dir, "customerB", "bob")

	if err := os.MkdirAll(filepath.Join(dir, "customerA"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "customerA", "envoys.vgo"), `
envoys:
  - match: "a01"
`)

	if err := os.MkdirAll(filepath.Join(dir, "customerB"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "customerB", "envoys.vgo"), `
envoys:
  - match: "b01"
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfgA := cs.Get("a01")
	if cfgA == nil {
		t.Fatal("expected config for a01")
	}
	if !hasModule(cfgA.Modules, "admin") || !hasModule(cfgA.Modules, "alice") {
		t.Errorf("a01 should have admin + alice; got %v", moduleNames(cfgA.Modules))
	}
	if hasModule(cfgA.Modules, "bob") {
		t.Errorf("a01 must not pick up bob (customerB scope)")
	}

	cfgB := cs.Get("b01")
	if cfgB == nil {
		t.Fatal("expected config for b01")
	}
	if !hasModule(cfgB.Modules, "admin") || !hasModule(cfgB.Modules, "bob") {
		t.Errorf("b01 should have admin + bob; got %v", moduleNames(cfgB.Modules))
	}
	if hasModule(cfgB.Modules, "alice") {
		t.Errorf("b01 must not pick up alice (customerA scope)")
	}
}

func TestUsercrate_ExcludeOnMatchBlock(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "", "admin")
	writeUsercrateDir(t, dir, "", "devops")

	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
    exclude_usercrates: [devops]
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("plex")
	if !hasModule(cfg.Modules, "admin") {
		t.Error("admin should apply")
	}
	if hasModule(cfg.Modules, "devops") {
		t.Errorf("exclude_usercrates did not drop devops; modules = %v", moduleNames(cfg.Modules))
	}
}

func TestUsercrate_ExcludeOnCommon(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "", "admin")
	writeUsercrateDir(t, dir, "", "devops")

	if err := os.MkdirAll(filepath.Join(dir, "rack1"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "rack1", "common.vgo"), `
exclude_usercrates: [devops]
`)
	writeFile(t, filepath.Join(dir, "rack1", "envoys.vgo"), `
envoys:
  - match: "rack1-*"
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("rack1-01")
	if !hasModule(cfg.Modules, "admin") {
		t.Error("admin should still apply (not excluded)")
	}
	if hasModule(cfg.Modules, "devops") {
		t.Errorf("common.vgo exclude_usercrates failed to drop devops; modules = %v", moduleNames(cfg.Modules))
	}
}

// writeUsercrateDirNoName writes a usercrate file with no `name:` field,
// exercising the filename-stem default in the loader.
func writeUsercrateDirNoName(t *testing.T, dir, scope, filenameStem string) {
	t.Helper()
	usercrateDir := filepath.Join(dir, scope, "usercrates")
	if err := os.MkdirAll(usercrateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`
resources:
  - name: create-%s
    type: user
    username: %s
`, filenameStem, filenameStem)
	writeFile(t, filepath.Join(usercrateDir, filenameStem+".vgo"), content)
}

func TestUsercrate_DefaultsNameFromFilename(t *testing.T) {
	dir := setupInheritanceDir(t)
	// usercrates/alice.vgo has no explicit name — loader must default
	// it to the filename stem "alice".
	writeUsercrateDirNoName(t, dir, "", "alice")

	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("plex")
	if cfg == nil {
		t.Fatal("expected config")
	}
	if !hasModule(cfg.Modules, "alice") {
		t.Errorf("usercrate with defaulted name did not apply as `alice`; modules = %v", moduleNames(cfg.Modules))
	}
}

func TestUsercrate_DefaultedNameHonorsExcludeUsercrates(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDirNoName(t, dir, "", "alice")
	writeUsercrateDirNoName(t, dir, "", "bob")

	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
    exclude_usercrates: [alice]
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("plex")
	if hasModule(cfg.Modules, "alice") {
		t.Errorf("exclude_usercrates [alice] should drop the defaulted-name crate; modules = %v", moduleNames(cfg.Modules))
	}
	if !hasModule(cfg.Modules, "bob") {
		t.Errorf("bob should still apply; modules = %v", moduleNames(cfg.Modules))
	}
}

func TestUsercrate_NameCollisionWithModule(t *testing.T) {
	dir := setupInheritanceDir(t)
	// Both a regular module and a usercrate named "dan" — must fail to
	// load with a clear error message.
	writeFile(t, filepath.Join(dir, "modules", "dan.vgo"), `
name: dan
resources:
  - name: placeholder
    type: package
    package: bash
`)
	writeUsercrateDir(t, dir, "", "dan")

	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err == nil {
		t.Fatal("expected name-collision error, got nil")
	}
	if !strings.Contains(err.Error(), "collision") || !strings.Contains(err.Error(), "dan") {
		t.Errorf("error should mention collision and the name; got: %v", err)
	}
}

func TestUsercrate_TraceShowsUsercrateSource(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "customerA", "alice")

	if err := os.MkdirAll(filepath.Join(dir, "customerA"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "customerA", "envoys.vgo"), `
envoys:
  - match: "a01"
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	trace := cs.Trace("a01")
	src, ok := trace.ModuleSources["alice"]
	if !ok {
		t.Fatalf("trace.ModuleSources missing entry for alice; keys = %v", mapKeys(trace.ModuleSources))
	}
	if !strings.Contains(src, "usercrates") {
		t.Errorf("alice source should reference usercrates/ path; got %q", src)
	}
	// At least one InheritanceLevel should carry the "usercrates/" marker.
	found := false
	for _, lvl := range trace.Inheritance {
		if lvl.File == "usercrates/" {
			found = true
			break
		}
	}
	if !found {
		t.Error("trace.Inheritance should include a usercrates/ level")
	}
}

// --- helpers ---

func hasModule(mods []ModuleConfig, name string) bool {
	for _, m := range mods {
		if m.Name == name {
			return true
		}
	}
	return false
}

func moduleNames(mods []ModuleConfig) []string {
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, m.Name)
	}
	return out
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
