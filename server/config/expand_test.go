// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"testing"
)

// --- ExpandForEach tests ---

func TestExpandForEach_BasicExpansion(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name: "vimrc-{{ .Item }}",
			Type: "file",
			Attributes: map[string]any{
				"target_path": "/home/{{ .Item }}/.vimrc",
				"owner":      "{{ .Item }}",
				"content":    "set number",
			},
			ForEach: "users",
		},
	}
	vars := map[string]any{
		"users": []any{"dan", "alice", "bob"},
	}

	result, err := ExpandForEach(resources, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}

	for i, name := range []string{"vimrc-dan", "vimrc-alice", "vimrc-bob"} {
		if result[i].Name != name {
			t.Errorf("result[%d].Name = %q, want %q", i, result[i].Name, name)
		}
	}

	if result[0].Attributes["target_path"] != "/home/dan/.vimrc" {
		t.Errorf("result[0].targetpath = %v", result[0].Attributes["target_path"])
	}
	if result[1].Attributes["owner"] != "alice" {
		t.Errorf("result[1].owner = %v", result[1].Attributes["owner"])
	}
}

func TestExpandForEach_SubstitutionInNameAndWhen(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name:    "cfg-{{ .Item }}",
			Type:    "file",
			When:    "user_exists('{{ .Item }}')",
			ForEach: "users",
			Attributes: map[string]any{
				"target_path": "/home/{{ .Item }}/.config",
			},
		},
	}
	vars := map[string]any{"users": []any{"dan"}}

	result, err := ExpandForEach(resources, vars)
	if err != nil {
		t.Fatal(err)
	}
	if result[0].Name != "cfg-dan" {
		t.Errorf("name = %q", result[0].Name)
	}
	if result[0].When != "user_exists('dan')" {
		t.Errorf("when = %q", result[0].When)
	}
}

func TestExpandForEach_EmptyList(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name:       "cfg-{{ .Item }}",
			Type:       "file",
			ForEach:    "users",
			Attributes: map[string]any{},
		},
	}
	vars := map[string]any{"users": []any{}}

	result, err := ExpandForEach(resources, vars)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0", len(result))
	}
}

func TestExpandForEach_MissingVar(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name:       "cfg-{{ .Item }}",
			Type:       "file",
			ForEach:    "nonexistent",
			Attributes: map[string]any{},
		},
	}
	_, err := ExpandForEach(resources, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing var")
	}
}

func TestExpandForEach_VarNotList(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name:       "cfg-{{ .Item }}",
			Type:       "file",
			ForEach:    "port",
			Attributes: map[string]any{},
		},
	}
	_, err := ExpandForEach(resources, map[string]any{"port": "8080"})
	if err == nil {
		t.Fatal("expected error for non-list var")
	}
}

func TestExpandForEach_MixedResources(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name:       "static",
			Type:       "package",
			Attributes: map[string]any{"package": "curl"},
		},
		{
			Name:    "cfg-{{ .Item }}",
			Type:    "file",
			ForEach: "users",
			Attributes: map[string]any{
				"target_path": "/home/{{ .Item }}/.rc",
			},
		},
		{
			Name:       "another-static",
			Type:       "service",
			Attributes: map[string]any{"service": "nginx"},
		},
	}
	vars := map[string]any{"users": []any{"dan", "alice"}}

	result, err := ExpandForEach(resources, vars)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 4 {
		t.Fatalf("len(result) = %d, want 4", len(result))
	}
	if result[0].Name != "static" {
		t.Errorf("result[0].Name = %q, want static", result[0].Name)
	}
	if result[1].Name != "cfg-dan" {
		t.Errorf("result[1].Name = %q, want cfg-dan", result[1].Name)
	}
	if result[2].Name != "cfg-alice" {
		t.Errorf("result[2].Name = %q, want cfg-alice", result[2].Name)
	}
	if result[3].Name != "another-static" {
		t.Errorf("result[3].Name = %q, want another-static", result[3].Name)
	}
}

func TestExpandForEach_PreservesStructuralFields(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name:       "svc-{{ .Item }}",
			Type:       "service",
			ForEach:    "services",
			DependsOn:  []string{"install-pkg"},
			Notify:     []string{"restart-handler"},
			Attributes: map[string]any{"service": "{{ .Item }}"},
		},
	}
	vars := map[string]any{"services": []any{"nginx", "redis"}}

	result, err := ExpandForEach(resources, vars)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
	for i, r := range result {
		if len(r.DependsOn) != 1 || r.DependsOn[0] != "install-pkg" {
			t.Errorf("result[%d].DependsOn = %v", i, r.DependsOn)
		}
		if len(r.Notify) != 1 || r.Notify[0] != "restart-handler" {
			t.Errorf("result[%d].Notify = %v", i, r.Notify)
		}
		if r.ForEach != "" {
			t.Errorf("result[%d].ForEach should be cleared", i)
		}
	}
}

func TestExpandForEach_NoWhitespace(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name:       "svc-{{.Item}}",
			Type:       "service",
			ForEach:    "names",
			Attributes: map[string]any{"service": "{{.Item}}"},
		},
	}
	vars := map[string]any{"names": []any{"foo"}}

	result, err := ExpandForEach(resources, vars)
	if err != nil {
		t.Fatal(err)
	}
	if result[0].Name != "svc-foo" {
		t.Errorf("name = %q", result[0].Name)
	}
}

// --- ExpandCase tests ---

func TestExpandCase_CorrectBranch(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name: "install-editor",
			Type: "package",
			Case: "{{ .Traits.os_family }}",
			CaseMatch: map[string]map[string]any{
				"debian":  {"package": "vim-nox"},
				"redhat":  {"package": "vim-enhanced"},
				"default": {"package": "vim"},
			},
			Attributes: map[string]any{},
		},
	}
	traits := map[string]any{"os_family": "debian"}

	result, err := ExpandCase(resources, traits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Attributes["package"] != "vim-nox" {
		t.Errorf("package = %v, want vim-nox", result[0].Attributes["package"])
	}
	if result[0].Case != "" {
		t.Error("Case should be cleared")
	}
	if result[0].CaseMatch != nil {
		t.Error("CaseMatch should be cleared")
	}
}

func TestExpandCase_DefaultFallback(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name: "install-editor",
			Type: "package",
			Case: "{{ .Traits.os_family }}",
			CaseMatch: map[string]map[string]any{
				"debian":  {"package": "vim-nox"},
				"default": {"package": "vim"},
			},
			Attributes: map[string]any{},
		},
	}
	traits := map[string]any{"os_family": "alpine"}

	result, err := ExpandCase(resources, traits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result[0].Attributes["package"] != "vim" {
		t.Errorf("package = %v, want vim", result[0].Attributes["package"])
	}
}

func TestExpandCase_EmptyTraitsUsesDefault(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name: "install-editor",
			Type: "package",
			Case: "{{ .Traits.os_family }}",
			CaseMatch: map[string]map[string]any{
				"debian":  {"package": "vim-nox"},
				"default": {"package": "vim"},
			},
			Attributes: map[string]any{},
		},
	}

	result, err := ExpandCase(resources, map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result[0].Attributes["package"] != "vim" {
		t.Errorf("package = %v, want vim (should fall through to default)", result[0].Attributes["package"])
	}
}

func TestExpandCase_BranchOverridesExistingAttr(t *testing.T) {
	resources := []ResourceConfig{
		{
			Name: "install-editor",
			Type: "package",
			Case: "{{ .Traits.os_family }}",
			CaseMatch: map[string]map[string]any{
				"debian":  {"package": "vim-nox"},
				"default": {"package": "vim"},
			},
			Attributes: map[string]any{
				"package": "vi",
				"state":   "present",
			},
		},
	}
	traits := map[string]any{"os_family": "debian"}

	result, err := ExpandCase(resources, traits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result[0].Attributes["package"] != "vim-nox" {
		t.Errorf("package = %v, want vim-nox (branch should override)", result[0].Attributes["package"])
	}
	if result[0].Attributes["state"] != "present" {
		t.Errorf("state = %v, want present (non-overridden attr preserved)", result[0].Attributes["state"])
	}
}

func TestExpandCase_PassthroughNonCaseResources(t *testing.T) {
	resources := []ResourceConfig{
		{Name: "plain", Type: "file", Attributes: map[string]any{"path": "/etc/foo"}},
		{
			Name: "editor",
			Type: "package",
			Case: "{{ .Traits.os_family }}",
			CaseMatch: map[string]map[string]any{
				"default": {"package": "vim"},
			},
			Attributes: map[string]any{},
		},
	}
	traits := map[string]any{}

	result, err := ExpandCase(resources, traits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
	if result[0].Name != "plain" {
		t.Errorf("result[0].Name = %q, want plain", result[0].Name)
	}
}

// --- HasCaseResources tests ---

func TestHasCaseResources_True(t *testing.T) {
	cfg := &EnvoyConfig{
		Modules: []ModuleConfig{
			{Resources: []ResourceConfig{{Name: "a"}}},
			{Resources: []ResourceConfig{{Name: "b", Case: "{{ .Traits.os }}"}}},
		},
	}
	if !HasCaseResources(cfg) {
		t.Error("expected true")
	}
}

func TestHasCaseResources_False(t *testing.T) {
	cfg := &EnvoyConfig{
		Modules: []ModuleConfig{
			{Resources: []ResourceConfig{{Name: "a"}, {Name: "b"}}},
		},
	}
	if HasCaseResources(cfg) {
		t.Error("expected false")
	}
}
