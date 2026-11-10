// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseConfigcrateFile_BasicConfigcrate(t *testing.T) {
	data := []byte(`
name: nginx
vars:
  port: "80"
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
  - name: nginx-service
    type: service
    service: nginx
    state: running
    subscribes: [nginx-conf]
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if def.Name != "nginx" {
		t.Errorf("name = %q, want nginx", def.Name)
	}
	if len(def.Resources) != 2 {
		t.Fatalf("len(resources) = %d, want 2", len(def.Resources))
	}

	r0 := def.Resources[0]
	if r0.Name != "install-nginx" || r0.Type != "package" {
		t.Errorf("resource 0: name=%q type=%q", r0.Name, r0.Type)
	}
	if r0.Attributes["package"] != "nginx" {
		t.Errorf("resource 0: package=%v, want nginx", r0.Attributes["package"])
	}

	r1 := def.Resources[1]
	if r1.Name != "nginx-service" || r1.Type != "service" {
		t.Errorf("resource 1: name=%q type=%q", r1.Name, r1.Type)
	}
	if len(r1.Subscribes) != 1 || r1.Subscribes[0] != "nginx-conf" {
		t.Errorf("resource 1: subscribes=%v, want [nginx-conf]", r1.Subscribes)
	}
}

func TestParseConfigcrateFile_DuplicateKeyRejected(t *testing.T) {
	data := []byte(`
name: docker
resources:
  - name: docker-repo
    type: repository
    name: docker
    key_url: https://example.com/key.gpg
    state: present
`)

	_, err := ParseConfigcrateFile(data)
	if err == nil {
		t.Fatal("expected error for duplicate key, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("error should mention duplicate key, got: %v", err)
	}
}

func TestParseConfigcrateFile_ExecWithGuards(t *testing.T) {
	data := []byte(`
name: setup
resources:
  - name: download-key
    type: exec
    command: "curl -fsSL https://example.com/key | gpg --dearmor -o /etc/key.gpg"
    unless: "test -f /etc/key.gpg"
    depends_on: [some-dep]
    notify: [some-service]
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := def.Resources[0]
	if r.Name != "download-key" {
		t.Errorf("name = %q", r.Name)
	}
	if r.Attributes["unless"] != "test -f /etc/key.gpg" {
		t.Errorf("unless = %v", r.Attributes["unless"])
	}
	if len(r.DependsOn) != 1 || r.DependsOn[0] != "some-dep" {
		t.Errorf("depends_on = %v", r.DependsOn)
	}
	if len(r.Notify) != 1 || r.Notify[0] != "some-service" {
		t.Errorf("notify = %v", r.Notify)
	}
}

func TestParseConfigcrateFile_BeforeAtConfigcrateLevel(t *testing.T) {
	data := []byte(`
name: base-packages
before: [app-config]
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if def.Name != "base-packages" {
		t.Errorf("name = %q, want base-packages", def.Name)
	}
	if len(def.Before) != 1 || def.Before[0] != "app-config" {
		t.Errorf("before = %v, want [app-config]", def.Before)
	}
}

func TestParseConfigcrateFile_BeforeAtResourceLevel(t *testing.T) {
	data := []byte(`
name: setup
resources:
  - name: create-dir
    type: exec
    command: "mkdir -p /opt/app"
    unless: "test -d /opt/app"
    before: [install-app]
  - name: install-app
    type: package
    package: myapp
    state: present
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(def.Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(def.Resources))
	}
	r0 := def.Resources[0]
	if len(r0.Before) != 1 || r0.Before[0] != "install-app" {
		t.Errorf("resource before = %v, want [install-app]", r0.Before)
	}
}

func TestParseConfigcrateFile_EmptyDocument(t *testing.T) {
	_, err := ParseConfigcrateFile([]byte(""))
	if err == nil {
		t.Fatal("expected error for empty document")
	}
}

// TestParseConfigcrateFile_RetiredTopLevelProvides pins the defensive
// hard-error from 0.66.72. modlint
// (rule_configcrate_top_level_provides_bundle) catches this at publish
// time, but parseConfigcrateYAML is now the parallel rejection point for
// direct-decode callers that bypass modlint. The matching
// UnmarshalYAML test below pins the second decode path.
func TestParseConfigcrateFile_RetiredTopLevelProvides(t *testing.T) {
	data := []byte(`
name: legacy
provides: [time-sync]
resources:
  - name: noop
    type: package
    package: chrony
    state: present
`)
	_, err := ParseConfigcrateFile(data)
	if err == nil {
		t.Fatal("expected error for top-level provides:")
	}
	for _, want := range []string{"retired", "0.66.57", "compliance:", "`provides:`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected %q in error, got: %v", want, err)
		}
	}
}

func TestParseConfigcrateFile_RetiredTopLevelBundle(t *testing.T) {
	data := []byte(`
name: legacy
bundle: [cis-ubuntu-access]
resources:
  - name: noop
    type: package
    package: nginx
    state: present
`)
	_, err := ParseConfigcrateFile(data)
	if err == nil {
		t.Fatal("expected error for top-level bundle:")
	}
	for _, want := range []string{"retired", "0.66.57", "compliance:", "`bundle:`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected %q in error, got: %v", want, err)
		}
	}
}

// TestParseConfigcrateFile_NestedComplianceShapeAccepted is the happy path:
// the 0.66.57 nested shape must still load cleanly. Pins that the
// retired-keys arm doesn't accidentally swallow valid keys.
func TestParseConfigcrateFile_NestedComplianceShapeAccepted(t *testing.T) {
	data := []byte(`
name: chrony
compliance:
  provides: [time-sync]
  bundle: [cis-ubuntu-access]
resources:
  - name: install-chrony
    type: package
    package: chrony
    state: present
`)
	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.Compliance.Provides) != 1 || def.Compliance.Provides[0] != "time-sync" {
		t.Errorf("Compliance.Provides = %v, want [time-sync]", def.Compliance.Provides)
	}
	if len(def.Compliance.Bundle) != 1 || def.Compliance.Bundle[0] != "cis-ubuntu-access" {
		t.Errorf("Compliance.Bundle = %v, want [cis-ubuntu-access]", def.Compliance.Bundle)
	}
}

// TestConfigcrateDefinition_UnmarshalYAML_RetiredTopLevelProvides pins the
// second decode path — the yaml.Unmarshaler implementation on
// ConfigcrateDefinition itself. Both paths must surface the same error so
// the rejection can't be smuggled past one of them.
func TestConfigcrateDefinition_UnmarshalYAML_RetiredTopLevelProvides(t *testing.T) {
	data := []byte(`
name: legacy
provides: [time-sync]
resources:
  - name: noop
    type: package
    package: chrony
    state: present
`)
	var def ConfigcrateDefinition
	err := yaml.Unmarshal(data, &def)
	if err == nil {
		t.Fatal("expected error for top-level provides: via UnmarshalYAML")
	}
	for _, want := range []string{"retired", "0.66.57", "compliance:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected %q in error, got: %v", want, err)
		}
	}
}

func TestParseConfigcrateFile_WatchSecret(t *testing.T) {
	data := []byte(`
name: grafana
resources:
  - name: grafana-set-password
    type: exec
    command: "grafana-cli admin reset-admin-password 'secret'"
    watch_secret: ["vigo/grafana/admin-password"]
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(def.Resources) != 1 {
		t.Fatalf("resources count = %d, want 1", len(def.Resources))
	}
	res := def.Resources[0]
	if len(res.WatchSecret) != 1 {
		t.Fatalf("watch_secret count = %d, want 1", len(res.WatchSecret))
	}
	if res.WatchSecret[0] != "vigo/grafana/admin-password" {
		t.Errorf("watch_secret[0] = %q, want %q", res.WatchSecret[0], "vigo/grafana/admin-password")
	}
}

func TestParseConfigcrateFile_DefaultsInherited(t *testing.T) {
	data := []byte(`
name: dotfiles
defaults:
  owner: dan
  group: dan
  mode: "0644"
resources:
  - name: vimrc
    type: file
    state: present
    target_path: "/home/dan/.vimrc"
    content: "set number"
  - name: bashrc
    type: file
    state: present
    target_path: "/home/dan/.bashrc"
    content: "export PS1='$ '"
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.Resources) != 2 {
		t.Fatalf("len(resources) = %d, want 2", len(def.Resources))
	}
	for i, r := range def.Resources {
		if r.Attributes["owner"] != "dan" {
			t.Errorf("resource %d: owner = %v, want dan", i, r.Attributes["owner"])
		}
		if r.Attributes["group"] != "dan" {
			t.Errorf("resource %d: group = %v, want dan", i, r.Attributes["group"])
		}
		if r.Attributes["mode"] != "0644" {
			t.Errorf("resource %d: mode = %v, want 0644", i, r.Attributes["mode"])
		}
	}
}

func TestParseConfigcrateFile_DefaultsOverridden(t *testing.T) {
	data := []byte(`
name: dotfiles
defaults:
  owner: dan
  mode: "0644"
resources:
  - name: vimrc
    type: file
    state: present
    target_path: "/home/dan/.vimrc"
    content: "set number"
  - name: script
    type: file
    state: present
    target_path: "/home/dan/run.sh"
    content: "#!/bin/sh"
    mode: "0755"
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.Resources[0].Attributes["mode"] != "0644" {
		t.Errorf("vimrc mode = %v, want 0644 (inherited)", def.Resources[0].Attributes["mode"])
	}
	if def.Resources[1].Attributes["mode"] != "0755" {
		t.Errorf("script mode = %v, want 0755 (overridden)", def.Resources[1].Attributes["mode"])
	}
	// Both should have inherited owner.
	if def.Resources[1].Attributes["owner"] != "dan" {
		t.Errorf("script owner = %v, want dan (inherited)", def.Resources[1].Attributes["owner"])
	}
}

func TestParseConfigcrateFile_DefaultsStructuralIgnored(t *testing.T) {
	data := []byte(`
name: test
defaults:
  notify: [should-be-ignored]
  depends_on: [should-be-ignored]
  owner: root
resources:
  - name: cfg
    type: file
    state: present
    target_path: /etc/test.conf
    content: "test"
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := def.Resources[0]
	if len(r.Notify) != 0 {
		t.Errorf("notify should not be set by defaults, got %v", r.Notify)
	}
	if len(r.DependsOn) != 0 {
		t.Errorf("depends_on should not be set by defaults, got %v", r.DependsOn)
	}
	if r.Attributes["owner"] != "root" {
		t.Errorf("owner = %v, want root (non-structural default)", r.Attributes["owner"])
	}
}

func TestParseConfigcrateFile_EmptyDefaults(t *testing.T) {
	data := []byte(`
name: test
defaults: {}
resources:
  - name: cfg
    type: file
    state: present
    target_path: /etc/test.conf
    content: "test"
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.Resources) != 1 {
		t.Fatalf("len(resources) = %d, want 1", len(def.Resources))
	}
}

func TestParseConfigcrateFile_Foreach(t *testing.T) {
	data := []byte(`
name: dotfiles
resources:
  - name: "vimrc-{{ .Item }}"
    type: file
    state: present
    foreach: users
    target_path: "/home/{{ .Item }}/.vimrc"
    content: "set number"
    owner: "{{ .Item }}"
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.Resources) != 1 {
		t.Fatalf("len(resources) = %d, want 1", len(def.Resources))
	}
	r := def.Resources[0]
	if r.ForEach != "users" {
		t.Errorf("foreach = %q, want users", r.ForEach)
	}
}

func TestParseConfigcrateFile_CaseWithMatch(t *testing.T) {
	data := []byte(`
name: editor
resources:
  - name: install-editor
    type: package
    state: present
    case: "{{ .Traits.os_family }}"
    match:
      debian:
        package: vim-nox
      redhat:
        package: vim-enhanced
      default:
        package: vim
`)

	def, err := ParseConfigcrateFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := def.Resources[0]
	if r.Case != "{{ .Traits.os_family }}" {
		t.Errorf("case = %q", r.Case)
	}
	if len(r.CaseMatch) != 3 {
		t.Fatalf("match branches = %d, want 3", len(r.CaseMatch))
	}
	if r.CaseMatch["debian"]["package"] != "vim-nox" {
		t.Errorf("debian package = %v", r.CaseMatch["debian"]["package"])
	}
	if r.CaseMatch["default"]["package"] != "vim" {
		t.Errorf("default package = %v", r.CaseMatch["default"]["package"])
	}
}

func TestParseConfigcrateFile_CaseWithoutMatch(t *testing.T) {
	data := []byte(`
name: test
resources:
  - name: bad
    type: package
    state: present
    case: "{{ .Traits.os_family }}"
`)

	_, err := ParseConfigcrateFile(data)
	if err == nil {
		t.Fatal("expected error when case has no match")
	}
	if !strings.Contains(err.Error(), "requires 'match'") {
		t.Errorf("error = %v, want mention of requires match", err)
	}
}

func TestParseConfigcrateFile_MatchWithoutDefault(t *testing.T) {
	data := []byte(`
name: test
resources:
  - name: bad
    type: package
    state: present
    case: "{{ .Traits.os_family }}"
    match:
      debian:
        package: vim-nox
`)

	_, err := ParseConfigcrateFile(data)
	if err == nil {
		t.Fatal("expected error when match has no default branch")
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("error = %v, want mention of default", err)
	}
}
