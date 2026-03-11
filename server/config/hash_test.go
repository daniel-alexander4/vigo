package config

import "testing"

func TestHashModuleContent_Deterministic(t *testing.T) {
	mod := ModuleConfig{
		Name:      "nginx",
		DependsOn: []string{"base"},
		When:      "is_web",
		Resources: []ResourceConfig{
			{
				Name: "conf",
				Type: "file",
				Attributes: map[string]any{
					"targetpath": "/etc/nginx/nginx.conf",
					"content":    "server { listen 80; }",
				},
				Notify: []string{"nginx-svc"},
			},
		},
	}

	hash1 := HashModuleContent(mod)
	hash2 := HashModuleContent(mod)

	if hash1 != hash2 {
		t.Errorf("hash not deterministic: %q != %q", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Errorf("hash length = %d, want 64 (SHA256 hex)", len(hash1))
	}
}

func TestHashModuleContent_DifferentContent(t *testing.T) {
	mod1 := ModuleConfig{
		Name:      "nginx",
		Resources: []ResourceConfig{{Name: "conf", Type: "file", Attributes: map[string]any{"content": "v1"}}},
	}
	mod2 := ModuleConfig{
		Name:      "nginx",
		Resources: []ResourceConfig{{Name: "conf", Type: "file", Attributes: map[string]any{"content": "v2"}}},
	}

	if HashModuleContent(mod1) == HashModuleContent(mod2) {
		t.Error("different content should produce different hashes")
	}
}

func TestHashModuleContent_AttributeOrderIrrelevant(t *testing.T) {
	mod := ModuleConfig{
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

	first := HashModuleContent(mod)
	for i := 0; i < 50; i++ {
		if h := HashModuleContent(mod); h != first {
			t.Fatalf("hash changed on iteration %d", i)
		}
	}
}

func TestHashModuleContent_WhenAffectsHash(t *testing.T) {
	base := ModuleConfig{
		Name:      "test",
		Resources: []ResourceConfig{{Name: "r", Type: "file", Attributes: map[string]any{"content": "x"}}},
	}

	withWhen := base
	withWhen.When = "is_production"

	if HashModuleContent(base) == HashModuleContent(withWhen) {
		t.Error("when expression should affect hash")
	}
}

func TestHashModuleContent_DepsAffectHash(t *testing.T) {
	base := ModuleConfig{
		Name:      "test",
		Resources: []ResourceConfig{{Name: "r", Type: "file", Attributes: map[string]any{"content": "x"}}},
	}

	withDeps := base
	withDeps.DependsOn = []string{"other"}

	if HashModuleContent(base) == HashModuleContent(withDeps) {
		t.Error("depends_on should affect hash")
	}
}
