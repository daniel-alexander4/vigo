// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

import "testing"

func TestHashConfigcrateContent_Deterministic(t *testing.T) {
	mod := Configcrate{
		Name:      "nginx",
		DependsOn: []string{"base"},
		When:      "is_web",
		Resources: []ResourceConfig{
			{
				Name: "conf",
				Type: "file",
				Attributes: map[string]any{
					"target_path": "/etc/nginx/nginx.conf",
					"content":     "server { listen 80; }",
				},
				Notify: []string{"nginx-svc"},
			},
		},
	}

	hash1 := HashConfigcrateContent(mod)
	hash2 := HashConfigcrateContent(mod)

	if hash1 != hash2 {
		t.Errorf("hash not deterministic: %q != %q", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Errorf("hash length = %d, want 64 (SHA256 hex)", len(hash1))
	}
}

func TestHashConfigcrateContent_DifferentContent(t *testing.T) {
	mod1 := Configcrate{
		Name:      "nginx",
		Resources: []ResourceConfig{{Name: "conf", Type: "file", Attributes: map[string]any{"content": "v1"}}},
	}
	mod2 := Configcrate{
		Name:      "nginx",
		Resources: []ResourceConfig{{Name: "conf", Type: "file", Attributes: map[string]any{"content": "v2"}}},
	}

	if HashConfigcrateContent(mod1) == HashConfigcrateContent(mod2) {
		t.Error("different content should produce different hashes")
	}
}

func TestHashConfigcrateContent_AttributeOrderIrrelevant(t *testing.T) {
	mod := Configcrate{
		Name: "test",
		Resources: []ResourceConfig{
			{
				Name: "r1",
				Type: "file",
				Attributes: map[string]any{
					"zzz": "last",
					"aaa": "first",
					"mmm": "middle",
				},
			},
		},
	}

	first := HashConfigcrateContent(mod)
	for i := 0; i < 50; i++ {
		if h := HashConfigcrateContent(mod); h != first {
			t.Fatalf("hash changed on iteration %d", i)
		}
	}
}

func TestHashConfigcrateContent_WhenAffectsHash(t *testing.T) {
	base := Configcrate{
		Name:      "test",
		Resources: []ResourceConfig{{Name: "r", Type: "file", Attributes: map[string]any{"content": "x"}}},
	}

	withWhen := base
	withWhen.When = "is_production"

	if HashConfigcrateContent(base) == HashConfigcrateContent(withWhen) {
		t.Error("when expression should affect hash")
	}
}

func TestHashConfigcrateContent_DepsAffectHash(t *testing.T) {
	base := Configcrate{
		Name:      "test",
		Resources: []ResourceConfig{{Name: "r", Type: "file", Attributes: map[string]any{"content": "x"}}},
	}

	withDeps := base
	withDeps.DependsOn = []string{"other"}

	if HashConfigcrateContent(base) == HashConfigcrateContent(withDeps) {
		t.Error("depends_on should affect hash")
	}
}

func TestHashConfigcrateContent_WatchSecretAffectsHash(t *testing.T) {
	base := Configcrate{
		Name: "test",
		Resources: []ResourceConfig{
			{Name: "r", Type: "exec", Attributes: map[string]any{"command": "reset-pw"}},
		},
	}

	withSecret := Configcrate{
		Name: "test",
		Resources: []ResourceConfig{
			{
				Name:        "r",
				Type:        "exec",
				Attributes:  map[string]any{"command": "reset-pw"},
				WatchSecret: []string{"vigo/grafana/admin-password"},
			},
		},
	}

	if HashConfigcrateContent(base) == HashConfigcrateContent(withSecret) {
		t.Error("watch_secret should affect hash")
	}
}
