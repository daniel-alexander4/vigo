// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnusedModules(t *testing.T) {
	dir := setupDir(t)

	// Three modules defined: nginx, sshd, orphan.
	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "sshd.vgo"), `
name: sshd
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "orphan.vgo"), `
name: orphan
resources: []
`)

	// Role references nginx.
	writeRoles(t, dir, `
- name: web
  modules: [nginx]
`)

	// Entry references sshd directly.
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web-*.example.com"
    roles: [web]
  - match: "db-*.example.com"
    modules: [sshd]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	result := store.Unused([]string{"web-01.example.com", "db-01.example.com"})

	// Only "orphan" should be unused.
	if len(result.UnusedModules) != 1 {
		t.Fatalf("expected 1 unused module, got %d: %+v", len(result.UnusedModules), result.UnusedModules)
	}
	if result.UnusedModules[0].Name != "orphan" {
		t.Errorf("expected unused module 'orphan', got %q", result.UnusedModules[0].Name)
	}
	if result.UnusedModules[0].SourceFile != "modules/orphan.vgo" {
		t.Errorf("expected source file 'modules/orphan.vgo', got %q", result.UnusedModules[0].SourceFile)
	}

	// No unused hostcrates — both patterns match enrolled hostnames.
	if len(result.UnusedHostcrates) != 0 {
		t.Errorf("expected 0 unused hostcrates, got %d: %+v", len(result.UnusedHostcrates), result.UnusedHostcrates)
	}
}

func TestUnusedHostcrates(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeRoles(t, dir, `
- name: web
  modules: [nginx]
`)

	// Two hostcrates: one matches enrolled hosts, one doesn't.
	writeFile(t, filepath.Join(dir, "default", "active.vgo"), `
envoys:
  - match: "web-*.example.com"
    roles: [web]
`)
	writeFile(t, filepath.Join(dir, "default", "retired.vgo"), `
envoys:
  - match: "old-*.retired.example.com"
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// Only web-01 is enrolled — "retired" file's patterns match nothing.
	result := store.Unused([]string{"web-01.example.com"})

	if len(result.UnusedHostcrates) != 1 {
		t.Fatalf("expected 1 unused hostcrate, got %d: %+v", len(result.UnusedHostcrates), result.UnusedHostcrates)
	}
	if result.UnusedHostcrates[0].Path != "default/retired.vgo" {
		t.Errorf("expected path 'default/retired.vgo', got %q", result.UnusedHostcrates[0].Path)
	}
}

func TestUnusedCommonModulesNotFlagged(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "monitoring.vgo"), `
name: monitoring
resources: []
`)

	writeRoles(t, dir, `
- name: empty
  modules: []
`)

	// base is referenced via common.vgo, monitoring is orphaned.
	writeFile(t, filepath.Join(dir, "common.vgo"), `
modules: [base]
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "host-*.example.com"
    roles: [empty]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	result := store.Unused([]string{"host-01.example.com"})

	if len(result.UnusedModules) != 1 {
		t.Fatalf("expected 1 unused module, got %d: %+v", len(result.UnusedModules), result.UnusedModules)
	}
	if result.UnusedModules[0].Name != "monitoring" {
		t.Errorf("expected 'monitoring' to be unused, got %q", result.UnusedModules[0].Name)
	}
}

func TestUnusedRoleCaseModulesNotFlagged(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "ssl.vgo"), `
name: ssl
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "debug.vgo"), `
name: debug
resources: []
`)

	// ssl is referenced in a role case block — should not be flagged.
	writeRoles(t, dir, `
- name: web
  modules: []
  case:
    - when: "is_prod"
      modules: [ssl]
`)

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*.example.com"
    roles: [web]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	result := store.Unused([]string{"web-01.example.com"})

	// Only debug should be unused; ssl is referenced by role case.
	if len(result.UnusedModules) != 1 {
		t.Fatalf("expected 1 unused module, got %d: %+v", len(result.UnusedModules), result.UnusedModules)
	}
	if result.UnusedModules[0].Name != "debug" {
		t.Errorf("expected 'debug' to be unused, got %q", result.UnusedModules[0].Name)
	}
}

func TestUnusedUsercrates(t *testing.T) {
	dir := setupDir(t)

	// Two usercrates defined: alice (referenced), bob (orphan).
	writeFile(t, filepath.Join(dir, "usercrates", "alice.vgo"), `
name: alice
resources:
  - name: alice
    type: user
    username: alice
`)
	writeFile(t, filepath.Join(dir, "usercrates", "bob.vgo"), `
name: bob
resources:
  - name: bob
    type: user
    username: bob
`)
	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [base]
    usercrates: [alice]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	result := store.Unused([]string{"host-01.example.com"})

	if len(result.UnusedUsercrates) != 1 {
		t.Fatalf("expected 1 unused usercrate, got %d: %+v", len(result.UnusedUsercrates), result.UnusedUsercrates)
	}
	if result.UnusedUsercrates[0].Name != "bob" {
		t.Errorf("expected 'bob' to be unused, got %q", result.UnusedUsercrates[0].Name)
	}
	// alice is in usercrates — never in UnusedModules.
	for _, m := range result.UnusedModules {
		if m.Name == "alice" || m.Name == "bob" {
			t.Errorf("usercrate %q surfaced as unused module — module/usercrate channels must be separate", m.Name)
		}
	}
}

func TestUnusedTemplates_LiteralReferencesStay(t *testing.T) {
	dir := setupDir(t)

	if err := os.MkdirAll(filepath.Join(dir, "templates", "nginx"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "templates", "nginx", "site.tmpl"), "server { listen 80; }\n")
	writeFile(t, filepath.Join(dir, "templates", "orphan.tmpl"), "unused content\n")

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: site
    type: file
    target_path: /etc/nginx/conf.d/site
    source: templates/nginx/site.tmpl
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web*.example.com"
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	result := store.Unused(nil)

	if len(result.UnusedTemplates) != 1 {
		t.Fatalf("expected 1 unused template, got %d: %+v", len(result.UnusedTemplates), result.UnusedTemplates)
	}
	if result.UnusedTemplates[0].Path != "templates/orphan.tmpl" {
		t.Errorf("expected 'templates/orphan.tmpl', got %q", result.UnusedTemplates[0].Path)
	}
}

func TestUnusedNothingUnused(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeRoles(t, dir, `
- name: web
  modules: [nginx]
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web-*.example.com"
    roles: [web]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	result := store.Unused([]string{"web-01.example.com"})

	if len(result.UnusedModules) != 0 {
		t.Errorf("expected 0 unused modules, got %d", len(result.UnusedModules))
	}
	if len(result.UnusedHostcrates) != 0 {
		t.Errorf("expected 0 unused hostcrates, got %d", len(result.UnusedHostcrates))
	}
}
