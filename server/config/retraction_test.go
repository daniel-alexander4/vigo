// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// resourceState returns the resolved value of a resource's attribute, failing
// the test if the configcrate or resource isn't present in the config.
func resourceAttr(t *testing.T, cfg *EnvoyConfig, configcrate, resource, attr string) any {
	t.Helper()
	for _, m := range cfg.Configcrates {
		if m.Name != configcrate {
			continue
		}
		for _, r := range m.Resources {
			if r.Name == resource {
				return r.Attributes[attr]
			}
		}
	}
	t.Fatalf("resource %s/%s not found in resolved config", configcrate, resource)
	return nil
}

// TestRetractResourceInPlace exercises the per-resource transform across every
// reversal class, including the error cases.
func TestRetractResourceInPlace(t *testing.T) {
	t.Run("state type flips to absent", func(t *testing.T) {
		r := &ResourceConfig{Name: "f", Type: "file", Attributes: map[string]any{"target_path": "/etc/x", "state": "present"}}
		if _, err := retractResourceInPlace(r); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.Attributes["state"] != "absent" {
			t.Errorf("file state = %v, want absent", r.Attributes["state"])
		}
	})

	t.Run("service stops and disables", func(t *testing.T) {
		r := &ResourceConfig{Name: "s", Type: "service", Attributes: map[string]any{"service": "nginx", "state": "running"}}
		if _, err := retractResourceInPlace(r); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.Attributes["state"] != "stopped" {
			t.Errorf("service state = %v, want stopped", r.Attributes["state"])
		}
		if r.Attributes["enabled"] != false {
			t.Errorf("service enabled = %v, want false", r.Attributes["enabled"])
		}
	})

	t.Run("command type needs on_revert", func(t *testing.T) {
		r := &ResourceConfig{Name: "e", Type: "exec", Attributes: map[string]any{"command": "touch /x"}}
		_, err := retractResourceInPlace(r)
		if err == nil || !strings.Contains(err.Error(), "on_revert") {
			t.Fatalf("expected on_revert error, got %v", err)
		}
	})

	t.Run("command type with on_revert reverts", func(t *testing.T) {
		r := &ResourceConfig{Name: "e", Type: "exec", Attributes: map[string]any{"command": "touch /x", "on_revert": "rm /x"}}
		if _, err := retractResourceInPlace(r); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.Attributes["revert"] != true {
			t.Errorf("exec revert = %v, want true", r.Attributes["revert"])
		}
	})

	t.Run("command with on_revert: none is omitted", func(t *testing.T) {
		r := &ResourceConfig{Name: "e", Type: "exec", Attributes: map[string]any{"command": "ufw enable", "on_revert": "none"}}
		omit, err := retractResourceInPlace(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !omit {
			t.Errorf("on_revert: none should omit the resource, got omit=false")
		}
		if r.Attributes["revert"] == true {
			t.Errorf("omitted resource must not be marked revert: true")
		}
	})

	t.Run("value setter reverts via snapshot", func(t *testing.T) {
		r := &ResourceConfig{Name: "h", Type: "hostname", Attributes: map[string]any{"hostname": "web01"}}
		if _, err := retractResourceInPlace(r); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.Attributes["revert"] != true {
			t.Errorf("hostname revert = %v, want true", r.Attributes["revert"])
		}
	})

	t.Run("non-reversible type errors legibly", func(t *testing.T) {
		r := &ResourceConfig{Name: "iface", Type: "cisco_interface", Attributes: map[string]any{}}
		_, err := retractResourceInPlace(r)
		if err == nil || !strings.Contains(err.Error(), "not reversible") {
			t.Fatalf("expected not-reversible error, got %v", err)
		}
		if !strings.Contains(err.Error(), "cisco_interface") {
			t.Errorf("error should name the type: %v", err)
		}
	})
}

// retractStack writes a docker-like configcrate (package + service) and returns
// the stacks dir; callers add the envoys.vgo / common.vgo / roles.vgo.
func retractStack(t *testing.T) string {
	t.Helper()
	dir := setupInheritanceDir(t)
	writeFile(t, filepath.Join(dir, "configcrates", "dockercrate.vgo"), `
name: dockercrate
resources:
  - name: docker-pkg
    type: package
    state: present
    package: docker.io
  - name: docker-svc
    type: service
    state: running
    service: docker
`)
	return dir
}

// TestConfigcrateRetraction_MatchBlock verifies a match-block state: absent
// retracts every resource, while a bare ref on another envoy is untouched.
func TestConfigcrateRetraction_MatchBlock(t *testing.T) {
	dir := retractStack(t)
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "retract.example.com"
    configcrates:
      - {name: dockercrate, state: absent}
  - match: "keep.example.com"
    configcrates: [dockercrate]
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	retract := cs.Get("retract.example.com")
	if got := resourceAttr(t, retract, "dockercrate", "docker-pkg", "state"); got != "absent" {
		t.Errorf("retracted package state = %v, want absent", got)
	}
	if got := resourceAttr(t, retract, "dockercrate", "docker-svc", "state"); got != "stopped" {
		t.Errorf("retracted service state = %v, want stopped", got)
	}

	keep := cs.Get("keep.example.com")
	if got := resourceAttr(t, keep, "dockercrate", "docker-pkg", "state"); got != "present" {
		t.Errorf("kept package state = %v, want present", got)
	}
}

// TestConfigcrateRetraction_OnRevertNoneOmitsAndScrubs verifies that an
// on_revert: none command is omitted from the retracted configcrate AND that a
// surviving resource's depends_on no longer references it — otherwise the
// agent's DAG build (a missing depends_on target is fatal) would abort the whole
// convergence.
func TestConfigcrateRetraction_OnRevertNoneOmitsAndScrubs(t *testing.T) {
	dir := setupInheritanceDir(t)
	writeFile(t, filepath.Join(dir, "configcrates", "hardencrate.vgo"), `
name: hardencrate
resources:
  - name: drop-conf
    type: file
    state: present
    target_path: /etc/app/cis.conf
    content: "x"
    owner: root
    group: root
    mode: "0644"
  - name: apply-perms
    type: exec
    command: chmod 0600 /etc/app/cis.conf
    onlyif: "true"
    on_revert: none
    depends_on: [drop-conf]
  - name: reload-app
    type: service
    state: running
    service: app
    depends_on: [apply-perms]
`)
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "retract.example.com"
    configcrates:
      - {name: hardencrate, state: absent}
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("retract.example.com")

	var mod *Configcrate
	for i := range cfg.Configcrates {
		if cfg.Configcrates[i].Name == "hardencrate" {
			mod = &cfg.Configcrates[i]
		}
	}
	if mod == nil {
		t.Fatal("hardencrate not resolved")
	}

	for _, r := range mod.Resources {
		if r.Name == "apply-perms" {
			t.Fatalf("on_revert: none resource should be omitted, but it is present")
		}
		// The surviving service depended on the omitted exec — that edge must be scrubbed.
		if r.Name == "reload-app" {
			for _, dep := range r.DependsOn {
				if dep == "apply-perms" {
					t.Errorf("reload-app still depends_on omitted apply-perms — agent DAG would abort")
				}
			}
		}
	}
	// And the surviving resources are genuinely retracted.
	if got := resourceAttr(t, cfg, "hardencrate", "drop-conf", "state"); got != "absent" {
		t.Errorf("drop-conf state = %v, want absent", got)
	}
}

// TestConfigcrateRetraction_CommonOverriddenByMatch verifies most-specific-wins:
// a common.vgo absent is un-retracted by a match-block present.
func TestConfigcrateRetraction_CommonOverriddenByMatch(t *testing.T) {
	dir := retractStack(t)
	writeFile(t, filepath.Join(dir, "common.vgo"), `
configcrates:
  - {name: dockercrate, state: absent}
`)
	writeFile(t, filepath.Join(dir, "web", "web.vgo"), `
envoys:
  - match: "exempt.example.com"
    configcrates:
      - {name: dockercrate, state: present}
  - match: "inherits.example.com"
    configcrates: []
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// match-block present (more specific) wins over common.vgo absent.
	exempt := cs.Get("exempt.example.com")
	if got := resourceAttr(t, exempt, "dockercrate", "docker-pkg", "state"); got != "present" {
		t.Errorf("exempt host package state = %v, want present (match > common)", got)
	}
	// no override → inherits common.vgo absent.
	inherits := cs.Get("inherits.example.com")
	if got := resourceAttr(t, inherits, "dockercrate", "docker-pkg", "state"); got != "absent" {
		t.Errorf("inheriting host package state = %v, want absent", got)
	}
}

// TestConfigcrateRetraction_RoleOverriddenByMatch verifies a role-provided
// configcrate (present) is retracted by a match-block absent.
func TestConfigcrateRetraction_RoleOverriddenByMatch(t *testing.T) {
	dir := retractStack(t)
	writeRoles(t, dir, `
- name: dockerrole
  configcrates: [dockercrate]
`)
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "host.example.com"
    roles: [dockerrole]
    configcrates:
      - {name: dockercrate, state: absent}
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("host.example.com")
	if got := resourceAttr(t, cfg, "dockercrate", "docker-pkg", "state"); got != "absent" {
		t.Errorf("package state = %v, want absent (match > role)", got)
	}
}

// TestConfigcrateRetraction_EnvironmentWins verifies environments.vgo is the
// most-specific carrier — its present un-retracts a match-block absent.
func TestConfigcrateRetraction_EnvironmentWins(t *testing.T) {
	dir := retractStack(t)
	writeFile(t, filepath.Join(dir, "environments.vgo"), `
env:
  prod:
    configcrates:
      - {name: dockercrate, state: present}
`)
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "host.example.com"
    environment: prod
    configcrates:
      - {name: dockercrate, state: absent}
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := cs.Get("host.example.com")
	if got := resourceAttr(t, cfg, "dockercrate", "docker-pkg", "state"); got != "present" {
		t.Errorf("package state = %v, want present (env > match)", got)
	}
}

// TestConfigcrateRetraction_InvalidState rejects a bad state value at publish.
func TestConfigcrateRetraction_InvalidState(t *testing.T) {
	dir := retractStack(t)
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "host.example.com"
    configcrates:
      - {name: dockercrate, state: gone}
`)
	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err == nil || !strings.Contains(err.Error(), "invalid state") {
		t.Fatalf("expected invalid-state error, got %v", err)
	}
}

// TestConfigcrateRetraction_UsercrateStateRejected rejects state: on a usercrate ref.
func TestConfigcrateRetraction_UsercrateStateRejected(t *testing.T) {
	dir := retractStack(t)
	writeFile(t, filepath.Join(dir, "usercrates", "admin.vgo"), `
name: admin
resources:
  - name: admin-user
    type: user
    state: present
    username: admin
`)
	writeFile(t, filepath.Join(dir, "envoys.vgo"), `
envoys:
  - match: "host.example.com"
    usercrates:
      - {name: admin, state: absent}
`)
	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err == nil || !strings.Contains(err.Error(), "usercrate") {
		t.Fatalf("expected usercrate-state error, got %v", err)
	}
}
