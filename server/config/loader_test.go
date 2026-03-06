package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mockProvider implements secrets.Provider for testing.
type mockProvider struct {
	secrets map[string]string
}

func (m *mockProvider) Get(_ context.Context, key string) (string, error) {
	v, ok := m.secrets[key]
	if !ok {
		return "", fmt.Errorf("secret %q not found", key)
	}
	return v, nil
}

func (m *mockProvider) GetBytes(ctx context.Context, key string) ([]byte, error) {
	v, err := m.Get(ctx, key)
	return []byte(v), err
}

func (m *mockProvider) Close() error { return nil }

// --- helpers ---

func setupTestDir(t *testing.T) (clientsDir, classificationPath string) {
	t.Helper()
	tmpDir := t.TempDir()
	clientsDir = filepath.Join(tmpDir, "clients")
	if err := os.MkdirAll(clientsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	classificationPath = filepath.Join(tmpDir, "classification.yaml")
	// Write empty classification by default
	if err := os.WriteFile(classificationPath, []byte("rules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return
}

func writeClientYAML(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- Tests ---

func TestLoadClientConfig(t *testing.T) {
	clientsDir, classPath := setupTestDir(t)
	writeClientYAML(t, clientsDir, "test.yaml", `
client: test.example.com
environment: production
modules:
  - name: nginx
    resources:
      - name: install-nginx
        type: package
        package: nginx
        state: present
`)
	mp := &mockProvider{secrets: map[string]string{}}
	store, err := NewConfigStore(clientsDir, classPath, mp)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := store.Get("test.example.com")
	if cfg == nil {
		t.Fatal("expected config for test.example.com, got nil")
	}
	if cfg.Client != "test.example.com" {
		t.Errorf("Client = %q, want test.example.com", cfg.Client)
	}
	if len(cfg.Modules) != 1 {
		t.Fatalf("len(Modules) = %d, want 1", len(cfg.Modules))
	}
	if cfg.Modules[0].Name != "nginx" {
		t.Errorf("Module name = %q, want nginx", cfg.Modules[0].Name)
	}
}

func TestSecretResolution(t *testing.T) {
	clientsDir, classPath := setupTestDir(t)
	writeClientYAML(t, clientsDir, "web.yaml", `
client: web.example.com
environment: production
vars:
  db_dsn: "secret:isogov/db/dsn"
modules:
  - name: app
    resources:
      - name: config
        type: file
        path: /etc/app/db.conf
        content: "secret:myapp/db/config"
`)
	mp := &mockProvider{secrets: map[string]string{
		"isogov/db/dsn":    "postgres://real:pw@db:5432/prod",
		"myapp/db/config":  "host=db port=5432 user=myapp",
	}}
	store, err := NewConfigStore(clientsDir, classPath, mp)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := store.Get("web.example.com")
	if cfg == nil {
		t.Fatal("expected config, got nil")
	}
	// Vars should have the resolved secret
	val, ok := cfg.Vars["db_dsn"]
	if !ok {
		t.Fatal("vars missing db_dsn")
	}
	if val != "postgres://real:pw@db:5432/prod" {
		t.Errorf("db_dsn = %q, want resolved DSN", val)
	}
}

func TestKnownSecretEnforcement(t *testing.T) {
	clientsDir, classPath := setupTestDir(t)
	// dsn without secret: prefix should fail
	writeClientYAML(t, clientsDir, "bad.yaml", `
client: bad.example.com
environment: production
vars:
  dsn: "postgres://plaintext@db/bad"
modules: []
`)
	mp := &mockProvider{secrets: map[string]string{}}
	_, err := NewConfigStore(clientsDir, classPath, mp)
	if err == nil {
		t.Fatal("expected error for known-secret field without secret: prefix, got nil")
	}
}

func TestHardFailUnresolvable(t *testing.T) {
	clientsDir, classPath := setupTestDir(t)
	writeClientYAML(t, clientsDir, "fail.yaml", `
client: fail.example.com
environment: production
vars:
  db_conn: "secret:nonexistent/key"
modules: []
`)
	mp := &mockProvider{secrets: map[string]string{}}
	_, err := NewConfigStore(clientsDir, classPath, mp)
	if err == nil {
		t.Fatal("expected error for unresolvable secret, got nil")
	}
}

func TestConfigWatcher(t *testing.T) {
	clientsDir, classPath := setupTestDir(t)
	mp := &mockProvider{secrets: map[string]string{}}
	store, err := NewConfigStore(clientsDir, classPath, mp)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go store.Watch(ctx)

	// Give the watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Initially unconfigured: empty environment (fallback config)
	if cfg := store.Get("new.example.com"); cfg.Environment != "" {
		t.Fatalf("expected empty environment before file write, got %q", cfg.Environment)
	}

	// Write a new client file
	writeClientYAML(t, clientsDir, "new.yaml", `
client: new.example.com
environment: staging
modules: []
`)

	// Wait for fsnotify + debounce (up to 2 seconds)
	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		cfg := store.Get("new.example.com")
		if cfg.Environment == "staging" {
			found = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Fatal("config not reloaded within 2 seconds after file write")
	}
}

func TestBadConfigKeepsOld(t *testing.T) {
	clientsDir, classPath := setupTestDir(t)
	writeClientYAML(t, clientsDir, "good.yaml", `
client: good.example.com
environment: production
modules: []
`)
	mp := &mockProvider{secrets: map[string]string{}}
	store, err := NewConfigStore(clientsDir, classPath, mp)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go store.Watch(ctx)
	time.Sleep(100 * time.Millisecond)

	// Verify good config exists
	if cfg := store.Get("good.example.com"); cfg == nil {
		t.Fatal("expected good config")
	}

	// Write invalid YAML over the file
	writeClientYAML(t, clientsDir, "good.yaml", `
client: good.example.com
  invalid yaml: [[[
`)

	// Wait for debounce
	time.Sleep(700 * time.Millisecond)

	// Good config should still be there
	if cfg := store.Get("good.example.com"); cfg == nil {
		t.Fatal("bad YAML replaced good config -- expected old config to be preserved")
	}
}

func TestClassification(t *testing.T) {
	clientsDir := t.TempDir()
	clientsDirInner := filepath.Join(clientsDir, "clients")
	if err := os.MkdirAll(clientsDirInner, 0o755); err != nil {
		t.Fatal(err)
	}
	classPath := filepath.Join(clientsDir, "classification.yaml")
	if err := os.WriteFile(classPath, []byte(`
rules:
  - match: "*.prod.example.com"
    environment: production
    modules:
      - name: base
      - name: nginx
  - match: "*.staging.example.com"
    environment: staging
    modules:
      - name: base
`), 0o644); err != nil {
		t.Fatal(err)
	}

	mp := &mockProvider{secrets: map[string]string{}}
	store, err := NewConfigStore(clientsDirInner, classPath, mp)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.prod.example.com")
	if cfg == nil {
		t.Fatal("expected classified config for web01.prod.example.com, got nil")
	}
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want production", cfg.Environment)
	}
	if len(cfg.Modules) != 2 {
		t.Fatalf("len(Modules) = %d, want 2", len(cfg.Modules))
	}
}

func TestExplicitOverClassification(t *testing.T) {
	clientsDir := t.TempDir()
	clientsDirInner := filepath.Join(clientsDir, "clients")
	if err := os.MkdirAll(clientsDirInner, 0o755); err != nil {
		t.Fatal(err)
	}
	classPath := filepath.Join(clientsDir, "classification.yaml")
	if err := os.WriteFile(classPath, []byte(`
rules:
  - match: "*.prod.example.com"
    environment: production
    modules:
      - name: classified-module
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Explicit config takes precedence
	writeClientYAML(t, clientsDirInner, "web01.yaml", `
client: web01.prod.example.com
environment: staging
modules:
  - name: explicit-module
`)

	mp := &mockProvider{secrets: map[string]string{}}
	store, err := NewConfigStore(clientsDirInner, classPath, mp)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.prod.example.com")
	if cfg == nil {
		t.Fatal("expected config, got nil")
	}
	if len(cfg.Modules) != 1 || cfg.Modules[0].Name != "explicit-module" {
		t.Errorf("expected explicit-module, got %v", cfg.Modules)
	}
	if cfg.Environment != "staging" {
		t.Errorf("Environment = %q, want staging (from explicit config)", cfg.Environment)
	}
}

func TestUnconfiguredClient(t *testing.T) {
	clientsDir, classPath := setupTestDir(t)
	mp := &mockProvider{secrets: map[string]string{}}
	store, err := NewConfigStore(clientsDir, classPath, mp)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("unknown.example.com")
	if cfg == nil {
		t.Fatal("expected non-nil config with empty modules for unconfigured client")
	}
	if len(cfg.Modules) != 0 {
		t.Errorf("expected empty modules, got %d", len(cfg.Modules))
	}
}

func TestEnvVarMerge(t *testing.T) {
	clientsDir, classPath := setupTestDir(t)
	writeClientYAML(t, clientsDir, "envtest.yaml", `
client: envtest.example.com
environment: production
vars:
  app_port: "8080"
  log_level: "info"
env_vars:
  production:
    app_port: "443"
    max_workers: "8"
  staging:
    app_port: "8443"
modules: []
`)
	mp := &mockProvider{secrets: map[string]string{}}
	store, err := NewConfigStore(clientsDir, classPath, mp)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("envtest.example.com")
	if cfg == nil {
		t.Fatal("expected config, got nil")
	}
	// env-specific "production" app_port should win
	if cfg.Vars["app_port"] != "443" {
		t.Errorf("app_port = %v, want 443 (env override)", cfg.Vars["app_port"])
	}
	// base log_level should survive
	if cfg.Vars["log_level"] != "info" {
		t.Errorf("log_level = %v, want info (base)", cfg.Vars["log_level"])
	}
	// env-specific max_workers should be present
	if cfg.Vars["max_workers"] != "8" {
		t.Errorf("max_workers = %v, want 8 (env override)", cfg.Vars["max_workers"])
	}
}

func TestTemplateRendering(t *testing.T) {
	facts := map[string]any{
		"os": map[string]any{"distro": "ubuntu"},
	}
	vars := map[string]any{
		"app_port": "8080",
	}

	result, err := RenderTemplate("port={{ .Vars.app_port }} os={{ .Facts.os.distro }}", facts, vars)
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if result != "port=8080 os=ubuntu" {
		t.Errorf("got %q, want 'port=8080 os=ubuntu'", result)
	}
}
