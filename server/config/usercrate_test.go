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
// <dir>/stacks/<scope>/usercrates/<name>.vgo. scope == "" means the
// root stacks/usercrates/ directory. The crate declares exactly one
// `type: user` resource (the 0.54.0 shape rule).
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
    state: present
    username: %s
`, name, name, name)
	writeFile(t, filepath.Join(usercrateDir, name+".vgo"), content)
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
    state: present
    username: %s
`, filenameStem, filenameStem)
	writeFile(t, filepath.Join(usercrateDir, filenameStem+".vgo"), content)
}

// --- Positive include path (5 tests) ---

func TestUsercrate_InertWhenNotIncluded(t *testing.T) {
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
	if hasConfigcrate(cfg.Configcrates, "admin") {
		t.Errorf("usercrate admin should NOT auto-apply; modules = %v", configcrateNames(cfg.Configcrates))
	}
}

func TestUsercrate_IncludedViaMatchBlock(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "", "admin")

	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
    usercrates:
      - admin
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("plex")
	if cfg == nil {
		t.Fatal("expected config")
	}
	if !hasConfigcrate(cfg.Configcrates, "admin") {
		t.Errorf("usercrate admin should be included via match-block usercrates:; modules = %v", configcrateNames(cfg.Configcrates))
	}
}

func TestUsercrate_IncludedViaCommon(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "", "admin")

	writeFile(t, filepath.Join(dir, "common.vgo"), `
usercrates:
  - admin
`)

	if err := os.MkdirAll(filepath.Join(dir, "branch"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "branch", "envoys.vgo"), `
envoys:
  - match: "a01"
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("a01")
	if cfg == nil {
		t.Fatal("expected config for a01")
	}
	if !hasConfigcrate(cfg.Configcrates, "admin") {
		t.Errorf("usercrate admin should be inherited via common.vgo usercrates:; modules = %v", configcrateNames(cfg.Configcrates))
	}
}

func TestUsercrate_IncludedViaEnvironment(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "", "admin")

	writeFile(t, filepath.Join(dir, "environments.vgo"), `
env:
  prod:
    usercrates:
      - admin
`)
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
    environment: "prod"
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("plex")
	if cfg == nil {
		t.Fatal("expected config")
	}
	if !hasConfigcrate(cfg.Configcrates, "admin") {
		t.Errorf("usercrate admin should be added by environments.vgo usercrates:; modules = %v", configcrateNames(cfg.Configcrates))
	}
}

func TestUsercrate_IncludedViaRole(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "", "admin")

	writeRoles(t, dir, `
- name: ops
  usercrates:
    - admin
`)
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
    roles:
      - ops
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("plex")
	if cfg == nil {
		t.Fatal("expected config")
	}
	if !hasConfigcrate(cfg.Configcrates, "admin") {
		t.Errorf("usercrate admin should be included via role usercrates:; modules = %v", configcrateNames(cfg.Configcrates))
	}
}

// --- Cross-kind rejection (2 tests) ---

func TestUsercrate_RejectsConfigcratesRefToUsercrate(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "", "admin")

	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
    configcrates:
      - admin
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err == nil {
		t.Fatal("expected cross-kind error for `configcrates: [admin]` referencing a usercrate, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "configcrates:") || !strings.Contains(msg, "usercrate") || !strings.Contains(msg, "admin") {
		t.Errorf("error should name the field, the kind, and the offending ref; got: %v", err)
	}
}

func TestUsercrate_RejectsUsercratesRefToConfigcrate(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeFile(t, filepath.Join(dir, "configcrates", "sshd.vgo"), `
name: sshd
resources:
  - name: install-sshd
    type: package
    package: openssh-server
    state: present
`)

	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
    usercrates:
      - sshd
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err == nil {
		t.Fatal("expected cross-kind error for `usercrates: [sshd]` referencing a configcrate, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "usercrates:") || !strings.Contains(msg, "configcrate") || !strings.Contains(msg, "sshd") {
		t.Errorf("error should name the field, the kind, and the offending ref; got: %v", err)
	}
}

// --- Shape rule (2 tests) ---

func TestUsercrate_RejectsUsercrateWithoutUserResource(t *testing.T) {
	dir := setupInheritanceDir(t)
	usercrateDir := filepath.Join(dir, "usercrates")
	if err := os.MkdirAll(usercrateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(usercrateDir, "broken.vgo"), `
name: broken
resources:
  - name: motd
    type: file
    state: present
    path: /etc/motd
    content: "hi"
`)
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err == nil {
		t.Fatal("expected shape-rule error for usercrate with 0 user resources, got nil")
	}
	if !strings.Contains(err.Error(), "exactly one") || !strings.Contains(err.Error(), "user") {
		t.Errorf("error should mention `exactly one` and `user`; got: %v", err)
	}
}

func TestUsercrate_AllowsUsercrateWithExtraNonUserResources(t *testing.T) {
	dir := setupInheritanceDir(t)
	usercrateDir := filepath.Join(dir, "usercrates")
	if err := os.MkdirAll(usercrateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(usercrateDir, "dan.vgo"), `
name: dan
resources:
  - name: create-dan
    type: user
    state: present
    username: dan
  - name: dan-authorized-keys
    type: file
    state: present
    path: /home/dan/.ssh/authorized_keys
    content: "ssh-ed25519 ..."
  - name: dan-shell-init
    type: exec
    command: "chsh -s /bin/bash dan"
`)
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
    usercrates:
      - dan
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("usercrate with user+file+exec should load; got: %v", err)
	}
	cfg := cs.Get("plex")
	if cfg == nil || !hasConfigcrate(cfg.Configcrates, "dan") {
		t.Errorf("usercrate dan should apply; modules = %v", configcrateNames(cfg.Configcrates))
	}
}

// --- Retired-field migration error (1 test) ---

func TestUsercrate_RetiredFieldRejectsExcludeUsercrates(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeUsercrateDir(t, dir, "", "admin")
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
    exclude_usercrates:
      - admin
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err == nil {
		t.Fatal("expected retired-field error for exclude_usercrates:, got nil")
	}
	if !strings.Contains(err.Error(), "removed in 0.54.0") || !strings.Contains(err.Error(), "exclude_usercrates") {
		t.Errorf("error should mention removal in 0.54.0 and name the field; got: %v", err)
	}
}

// --- Kept-and-adjusted (3 tests) ---

func TestUsercrate_DefaultsNameFromFilename(t *testing.T) {
	dir := setupInheritanceDir(t)
	// usercrates/alice.vgo has no explicit name — loader must default
	// it to the filename stem "alice".
	writeUsercrateDirNoName(t, dir, "", "alice")

	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "plex"
    usercrates:
      - alice
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("plex")
	if cfg == nil {
		t.Fatal("expected config")
	}
	if !hasConfigcrate(cfg.Configcrates, "alice") {
		t.Errorf("usercrate with defaulted name `alice` should resolve when included; modules = %v", configcrateNames(cfg.Configcrates))
	}
}

func TestUsercrate_NameCollisionWithConfigcrate(t *testing.T) {
	dir := setupInheritanceDir(t)
	// Both a regular module and a usercrate named "dan" — must fail to
	// load with a clear error message.
	writeFile(t, filepath.Join(dir, "configcrates", "dan.vgo"), `
name: dan
resources:
  - name: placeholder
    type: package
    package: bash
    state: present
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
    usercrates:
      - alice
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	trace := cs.Trace("a01")

	// ConfigcrateSources should point at the def's usercrates/<file>.vgo path
	// (the canonical SourceFile), not at the carrier that included it.
	src, ok := trace.ConfigcrateSources["alice"]
	if !ok {
		t.Fatalf("trace.ConfigcrateSources missing entry for alice; keys = %v", mapKeys(trace.ConfigcrateSources))
	}
	if !strings.Contains(src, "usercrates/alice.vgo") {
		t.Errorf("alice source should point at the def's usercrates/<file>.vgo; got %q", src)
	}

	// At least one InheritanceLevel should attribute alice via its
	// Usercrates list (step 7 Kind plumbing: usercrates are attributed
	// to inclusion sites, not a synthetic `usercrates/` level).
	var found bool
	for _, lvl := range trace.Inheritance {
		for _, u := range lvl.Usercrates {
			if u == "alice" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("trace.Inheritance should attribute alice to a level's Usercrates list")
	}
}

// --- helpers ---

func hasConfigcrate(mods []Configcrate, name string) bool {
	for _, m := range mods {
		if m.Name == name {
			return true
		}
	}
	return false
}

func configcrateNames(mods []Configcrate) []string {
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
