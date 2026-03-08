package selfmanage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_Enabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "self.hob")
	os.WriteFile(path, []byte(`
enabled: true
interval: 15m
resources:
  - name: test-user
    type: user
    username: hobnob
    system: "true"
  - name: test-file
    type: file
    path: /etc/hobnob
    ensure: directory
    mode: "0750"
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config, got nil")
	}
	if !cfg.Enabled {
		t.Error("expected enabled=true")
	}
	if cfg.Interval != "15m" {
		t.Errorf("interval = %q, want 15m", cfg.Interval)
	}
	if len(cfg.Resources) != 2 {
		t.Fatalf("resources = %d, want 2", len(cfg.Resources))
	}

	r := cfg.Resources[0]
	if r.Name != "test-user" || r.Type != "user" {
		t.Errorf("resource[0] = %s/%s, want test-user/user", r.Name, r.Type)
	}
	if r.Attributes["username"] != "hobnob" {
		t.Errorf("username = %v, want hobnob", r.Attributes["username"])
	}
}

func TestLoad_Disabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "self.hob")
	os.WriteFile(path, []byte("enabled: false\ninterval: 30m\nresources: []\n"), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Enabled {
		t.Error("expected enabled=false")
	}
}

func TestLoad_Missing(t *testing.T) {
	cfg, err := Load("/nonexistent/self.hob")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for missing file")
	}
}

func TestBuildModules(t *testing.T) {
	m := &Manager{
		cfg: &Config{
			Resources: []ResourceDef{
				{Name: "my-file", Type: "file", Attributes: map[string]any{"path": "/tmp/test", "mode": "0644"}},
				{Name: "my-pkg", Type: "package", Attributes: map[string]any{"package": "curl", "state": "present"}},
			},
		},
	}

	mods := m.buildModules()
	if len(mods) != 1 {
		t.Fatalf("modules = %d, want 1", len(mods))
	}
	if mods[0].Name != "self" {
		t.Errorf("module name = %q, want self", mods[0].Name)
	}
	if len(mods[0].Resources) != 2 {
		t.Fatalf("resources = %d, want 2", len(mods[0].Resources))
	}

	r := mods[0].Resources[0]
	if r.Name != "my-file" || r.Type != "file" {
		t.Errorf("resource = %s/%s, want my-file/file", r.Name, r.Type)
	}
	if r.Params["path"] != "/tmp/test" {
		t.Errorf("path = %q, want /tmp/test", r.Params["path"])
	}
	if r.Params["mode"] != "0644" {
		t.Errorf("mode = %q, want 0644", r.Params["mode"])
	}
}

func TestAttrsToParams(t *testing.T) {
	params := attrsToParams(map[string]any{
		"path":  "/etc/foo",
		"mode":  "0644",
		"count": 42,
		"flag":  true,
	})

	if params["path"] != "/etc/foo" {
		t.Errorf("path = %q", params["path"])
	}
	if params["mode"] != "0644" {
		t.Errorf("mode = %q", params["mode"])
	}
	if params["count"] != "42" {
		t.Errorf("count = %q", params["count"])
	}
	if params["flag"] != "true" {
		t.Errorf("flag = %q", params["flag"])
	}
}
