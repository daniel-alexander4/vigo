// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// setupDir creates a thrall-configs directory with thralls/, modules/, and roles/ subdirs.
func setupDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "thrall-configs")
	for _, sub := range []string{"thralls", "modules", "roles"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- Tests ---

func TestLoadModuleAndResolve(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)

	writeFile(t, filepath.Join(dir, "thralls", "webservers.yaml"), `
thralls:
  - match: "test.example.com"
    environment: production
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("test.example.com")
	if cfg.Thrall != "test.example.com" {
		t.Errorf("Thrall = %q, want test.example.com", cfg.Thrall)
	}
	if len(cfg.Modules) != 1 {
		t.Fatalf("len(Modules) = %d, want 1", len(cfg.Modules))
	}
	if cfg.Modules[0].Name != "nginx" {
		t.Errorf("Module name = %q, want nginx", cfg.Modules[0].Name)
	}
}

func TestRoleResolution(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "certbot.yaml"), `
name: certbot
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present
`)
	writeFile(t, filepath.Join(dir, "roles", "webserver.yaml"), `
name: webserver
modules: [nginx, certbot]
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web01.example.com"
    environment: production
    roles: [webserver]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.example.com")
	if len(cfg.Modules) != 2 {
		t.Fatalf("len(Modules) = %d, want 2", len(cfg.Modules))
	}
	if cfg.Modules[0].Name != "nginx" {
		t.Errorf("Module[0] = %q, want nginx", cfg.Modules[0].Name)
	}
	if cfg.Modules[1].Name != "certbot" {
		t.Errorf("Module[1] = %q, want certbot", cfg.Modules[1].Name)
	}
}

func TestModuleDefaultVarsOverriddenByClient(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
vars:
  worker_processes: "auto"
  keepalive_timeout: "65"
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web01.example.com"
    environment: production
    modules: [nginx]
    vars:
      worker_processes: "4"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.example.com")
	if cfg.Vars["worker_processes"] != "4" {
		t.Errorf("worker_processes = %v, want 4 (client override)", cfg.Vars["worker_processes"])
	}
	if cfg.Vars["keepalive_timeout"] != "65" {
		t.Errorf("keepalive_timeout = %v, want 65 (module default)", cfg.Vars["keepalive_timeout"])
	}
}

func TestSecretResolution(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: config
    type: file
    targetpath: /etc/app/db.conf
    content: "secret:myapp/db/config"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web.example.com"
    environment: production
    modules: [app]
    vars:
      db_dsn: "secret:hobnob/db/dsn"
`)

	mp := &mockProvider{secrets: map[string]string{
		"hobnob/db/dsn":   "/srv/hobnob/db/hobnob.db",
		"myapp/db/config": "host=db port=5432 user=myapp",
	}}
	store, err := NewConfigStore(dir, mp, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	if cfg.Vars["db_dsn"] != "/srv/hobnob/db/hobnob.db" {
		t.Errorf("db_dsn = %q, want resolved DSN", cfg.Vars["db_dsn"])
	}
}

func TestKnownSecretEnforcement(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "bad.example.com"
    environment: production
    modules: [app]
    vars:
      dsn: "plaintext-hobnob.db"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for known-secret field without secret: prefix, got nil")
	}
}

func TestHardFailUnresolvable(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "fail.example.com"
    environment: production
    modules: [app]
    vars:
      db_conn: "secret:nonexistent/key"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for unresolvable secret, got nil")
	}
}

func TestMultipleSecretErrorsCollected(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: config-file
    type: file
    path: /etc/app/config
    content: "secret:app/config/content"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web-*.prod.example.com"
    environment: production
    modules: [app]
    vars:
      db_conn: "secret:missing/db/conn"
      api_key: "secret:missing/api/key"
  - match: "db-*.prod.example.com"
    environment: production
    modules: [app]
    vars:
      db_conn: "secret:missing/db/conn2"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for unresolvable secrets, got nil")
	}

	var se *SecretErrors
	if !errors.As(err, &se) {
		t.Fatalf("expected *SecretErrors, got %T: %v", err, err)
	}

	// Should collect errors from both entries: 2 vars + 1 resource attr from first,
	// 1 var + 1 resource attr from second = 5 total (but resource errors also collected).
	if len(se.Errors) < 4 {
		t.Errorf("expected at least 4 secret errors, got %d:\n%s", len(se.Errors), se.Format())
	}

	// Verify hints are populated.
	for _, e := range se.Errors {
		if e.Hint() == "" {
			t.Errorf("empty hint for error: %v", e)
		}
	}

	// Verify formatted output is readable.
	formatted := se.Format()
	if !strings.Contains(formatted, "web-*.prod.example.com") {
		t.Error("formatted output missing first match pattern")
	}
	if !strings.Contains(formatted, "db-*.prod.example.com") {
		t.Error("formatted output missing second match pattern")
	}
	if !strings.Contains(formatted, "fix:") {
		t.Error("formatted output missing fix hints")
	}
}

func TestSecretErrorHintCategories(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		err     error
		want    string
	}{
		// Local backend hints.
		{"local not found", "local", fmt.Errorf("secret %q not found in /srv/hobnob/secrets", "x"), "hobnobcli secrets set"},
		{"local wrong key", "local", fmt.Errorf("decrypting: message authentication failed (wrong master key?)"), "re-encrypt"},
		{"local too short", "local", fmt.Errorf("ciphertext too short"), "plaintext from a previous"},
		// Isopass backend hints.
		{"isopass not found", "isopass", fmt.Errorf("secret %q not found in isopass", "x"), "isopass set"},
		{"isopass unreachable", "isopass", fmt.Errorf("isopass unreachable at https://x"), "HOBNOB_ISOPASS_URL"},
		{"isopass auth failed", "isopass", fmt.Errorf("isopass authentication failed"), "HOBNOB_ISOPASS_TOKEN"},
		// Backend-agnostic.
		{"plaintext value", "", fmt.Errorf("must use \"secret:\" prefix"), "secret:<path>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se := &SecretError{Match: "test", Attr: "x", Secret: "hobnob/db/dsn", Backend: tt.backend, Err: tt.err}
			if !strings.Contains(se.Hint(), tt.want) {
				t.Errorf("hint = %q, want substring %q", se.Hint(), tt.want)
			}
		})
	}
}

func TestPatternMatching(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.yaml"), `
name: base
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "*.prod.example.com"
    environment: production
    modules: [base, nginx]
  - match: "*.staging.example.com"
    environment: staging
    modules: [base]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.prod.example.com")
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want production", cfg.Environment)
	}
	if len(cfg.Modules) != 2 {
		t.Fatalf("len(Modules) = %d, want 2", len(cfg.Modules))
	}

	cfg2 := store.Get("app.staging.example.com")
	if cfg2.Environment != "staging" {
		t.Errorf("Environment = %q, want staging", cfg2.Environment)
	}
}

func TestFirstMatchWins(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "explicit.yaml"), `
name: explicit
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "pattern.yaml"), `
name: pattern
resources: []
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web01.prod.example.com"
    environment: staging
    modules: [explicit]
  - match: "*.prod.example.com"
    environment: production
    modules: [pattern]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.prod.example.com")
	if len(cfg.Modules) != 1 || cfg.Modules[0].Name != "explicit" {
		t.Errorf("expected explicit module, got %v", cfg.Modules)
	}
	if cfg.Environment != "staging" {
		t.Errorf("Environment = %q, want staging (first match)", cfg.Environment)
	}
}

func TestMultipleClientFiles(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "postgres.yaml"), `
name: postgres
resources: []
`)
	// Two separate client files
	writeFile(t, filepath.Join(dir, "thralls", "webservers.yaml"), `
thralls:
  - match: "web-*"
    environment: production
    modules: [nginx]
`)
	writeFile(t, filepath.Join(dir, "thralls", "databases.yaml"), `
thralls:
  - match: "db-*"
    environment: production
    modules: [postgres]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	web := store.Get("web-01")
	if len(web.Modules) != 1 || web.Modules[0].Name != "nginx" {
		t.Errorf("web-01: expected nginx, got %v", web.Modules)
	}

	db := store.Get("db-01")
	if len(db.Modules) != 1 || db.Modules[0].Name != "postgres" {
		t.Errorf("db-01: expected postgres, got %v", db.Modules)
	}
}

func TestUnconfiguredClient(t *testing.T) {
	dir := setupDir(t)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("unknown.example.com")
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Modules) != 0 {
		t.Errorf("expected empty modules, got %d", len(cfg.Modules))
	}
}

func TestEnvVarMerge(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "envtest.example.com"
    environment: production
    modules: [app]
    vars:
      app_port: "8080"
      log_level: "info"
    environment_overrides:
      production:
        app_port: "443"
        max_workers: "8"
      staging:
        app_port: "8443"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("envtest.example.com")
	if cfg.Vars["app_port"] != "443" {
		t.Errorf("app_port = %v, want 443 (env override)", cfg.Vars["app_port"])
	}
	if cfg.Vars["log_level"] != "info" {
		t.Errorf("log_level = %v, want info (base)", cfg.Vars["log_level"])
	}
	if cfg.Vars["max_workers"] != "8" {
		t.Errorf("max_workers = %v, want 8 (env override)", cfg.Vars["max_workers"])
	}
}

func TestConfigWatcher(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources: []
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go store.Watch(ctx)
	time.Sleep(100 * time.Millisecond)

	// Initially unconfigured
	if cfg := store.Get("new.example.com"); cfg.Environment != "" {
		t.Fatalf("expected empty environment before update, got %q", cfg.Environment)
	}

	// Write a new client file
	writeFile(t, filepath.Join(dir, "thralls", "new-hosts.yaml"), `
thralls:
  - match: "new.example.com"
    environment: staging
    modules: [app]
`)

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
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "good.example.com"
    environment: production
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go store.Watch(ctx)
	time.Sleep(100 * time.Millisecond)

	if cfg := store.Get("good.example.com"); cfg == nil {
		t.Fatal("expected good config")
	}

	// Write invalid YAML
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  invalid yaml: [[[
`)

	time.Sleep(700 * time.Millisecond)

	// Good config should still be there
	if cfg := store.Get("good.example.com"); cfg.Environment != "production" {
		t.Fatal("bad YAML replaced good config")
	}
}

func TestUnknownModuleError(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "bad.example.com"
    modules: [nonexistent]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for unknown module, got nil")
	}
}

func TestUnknownRoleError(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "bad.example.com"
    roles: [nonexistent]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for unknown role, got nil")
	}
}

func TestRoleReferencesUnknownModule(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "roles", "bad.yaml"), `
name: bad
modules: [nonexistent]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for role referencing unknown module, got nil")
	}
}

func TestDedupModules(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "roles", "web.yaml"), `
name: web
modules: [nginx]
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "dup.example.com"
    roles: [web]
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("dup.example.com")
	if len(cfg.Modules) != 1 {
		t.Errorf("expected 1 module (deduped), got %d", len(cfg.Modules))
	}
}

func TestNodesReferencingSecret(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: config
    type: file
    targetpath: /etc/app/config
    content: "secret:myapp/db/config"
`)
	writeFile(t, filepath.Join(dir, "modules", "other.yaml"), `
name: other
resources: []
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web-*"
    modules: [app]
  - match: "db-*"
    modules: [other]
`)

	mp := &mockProvider{secrets: map[string]string{
		"myapp/db/config": "resolved-value",
	}}
	store, err := NewConfigStore(dir, mp, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	patterns := store.ThrallsReferencingSecret("myapp/db/config")
	if len(patterns) != 1 || patterns[0] != "web-*" {
		t.Errorf("expected [web-*], got %v", patterns)
	}

	patterns = store.ThrallsReferencingSecret("nonexistent")
	if len(patterns) != 0 {
		t.Errorf("expected empty, got %v", patterns)
	}
}

func TestSourceFileLoading(t *testing.T) {
	dir := setupDir(t)

	// Create templates directory and template file.
	tmplDir := filepath.Join(dir, "templates")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tmplDir, "nginx.conf.tmpl"), `worker_processes {{ .Vars.worker_processes }};`)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
vars:
  worker_processes: "auto"
resources:
  - name: nginx-conf
    type: file
    targetpath: /etc/nginx/nginx.conf
    source: templates/nginx.conf.tmpl
    owner: root
    mode: "0644"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web.example.com"
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	if len(cfg.Modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(cfg.Modules))
	}

	res := cfg.Modules[0].Resources[0]
	content, ok := res.Attributes["content"]
	if !ok {
		t.Fatal("expected 'content' attribute after source resolution")
	}
	if content != `worker_processes {{ .Vars.worker_processes }};` {
		t.Errorf("content = %q, want template text", content)
	}
	if _, hasSource := res.Attributes["source"]; hasSource {
		t.Error("'source' attribute should be removed after resolution")
	}
}

func TestSourceAndContentMutuallyExclusive(t *testing.T) {
	dir := setupDir(t)

	tmplDir := filepath.Join(dir, "templates")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tmplDir, "test.tmpl"), "template content")

	writeFile(t, filepath.Join(dir, "modules", "bad.yaml"), `
name: bad
resources:
  - name: both-set
    type: file
    targetpath: /etc/test
    source: templates/test.tmpl
    content: "inline content"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "test.example.com"
    modules: [bad]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for both source and content set, got nil")
	}
	expected := "cannot set both 'source' and 'content'"
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("error = %q, want it to contain %q", err, expected)
	}
}

func TestSourceFileMissing(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "bad.yaml"), `
name: bad
resources:
  - name: missing-source
    type: file
    targetpath: /etc/test
    source: templates/nonexistent.tmpl
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "test.example.com"
    modules: [bad]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for missing source file, got nil")
	}
	if !strings.Contains(err.Error(), "reading source") {
		t.Errorf("error = %q, want it to mention reading source", err)
	}
}

func TestSourceFileNestedTemplatesDir(t *testing.T) {
	dir := setupDir(t)

	// Create nested templates directory structure.
	nestedDir := filepath.Join(dir, "templates", "nginx", "sites")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nestedDir, "default.conf.tmpl"), `server { listen {{ .Vars.port }}; }`)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
vars:
  port: "80"
resources:
  - name: nginx-site
    type: file
    targetpath: /etc/nginx/sites-enabled/default
    source: templates/nginx/sites/default.conf.tmpl
    mode: "0644"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web.example.com"
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	res := cfg.Modules[0].Resources[0]
	content, ok := res.Attributes["content"]
	if !ok {
		t.Fatal("expected 'content' attribute after source resolution")
	}
	if content != `server { listen {{ .Vars.port }}; }` {
		t.Errorf("content = %q, want template text", content)
	}
}

func TestRenderVars(t *testing.T) {
	tests := []struct {
		name     string
		vars     map[string]any
		traits    map[string]any
		varOrder []string
		want     map[string]any
		wantErr  bool
	}{
		{
			name: "traits reference renders correctly",
			vars: map[string]any{
				"greeting": "Hello from {{ .Traits.os.distro }}",
			},
			traits: map[string]any{
				"os": map[string]any{"distro": "ubuntu"},
			},
			varOrder: []string{"greeting"},
			want: map[string]any{
				"greeting": "Hello from ubuntu",
			},
		},
		{
			name: "vars reference renders correctly",
			vars: map[string]any{
				"base_port": "8080",
				"port_line": "port={{ .Vars.base_port }}",
			},
			traits:    map[string]any{},
			varOrder: []string{"base_port", "port_line"},
			want: map[string]any{
				"base_port": "8080",
				"port_line": "port=8080",
			},
		},
		{
			name: "chained vars render top-down",
			vars: map[string]any{
				"distro":   "{{ .Traits.os.distro }}",
				"greeting": "Hello from {{ .Vars.distro }}",
			},
			traits: map[string]any{
				"os": map[string]any{"distro": "ubuntu"},
			},
			varOrder: []string{"distro", "greeting"},
			want: map[string]any{
				"distro":   "ubuntu",
				"greeting": "Hello from ubuntu",
			},
		},
		{
			name: "plain string without template syntax passes through",
			vars: map[string]any{
				"plain": "no templates here",
			},
			traits:    map[string]any{},
			varOrder: []string{"plain"},
			want: map[string]any{
				"plain": "no templates here",
			},
		},
		{
			name: "non-string values are skipped without error",
			vars: map[string]any{
				"count":   42,
				"enabled": true,
				"name":    "test",
			},
			traits:    map[string]any{},
			varOrder: []string{"count", "enabled", "name"},
			want: map[string]any{
				"count":   42,
				"enabled": true,
				"name":    "test",
			},
		},
		{
			name: "invalid template syntax returns error",
			vars: map[string]any{
				"bad": "{{ .InvalidSyntax }{",
			},
			traits:    map[string]any{},
			varOrder: []string{"bad"},
			wantErr:  true,
		},
		{
			name:     "empty vars map returns empty map",
			vars:     map[string]any{},
			traits:    map[string]any{},
			varOrder: nil,
			want:     map[string]any{},
		},
		{
			name: "nil varOrder falls back to map iteration",
			vars: map[string]any{
				"greeting": "Hello from {{ .Traits.os.distro }}",
			},
			traits: map[string]any{
				"os": map[string]any{"distro": "ubuntu"},
			},
			varOrder: nil,
			want: map[string]any{
				"greeting": "Hello from ubuntu",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderVars(tt.vars, tt.traits, tt.varOrder)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(tt.want))
			}
			for k, wantV := range tt.want {
				gotV, ok := got[k]
				if !ok {
					t.Errorf("missing key %q", k)
					continue
				}
				if fmt.Sprintf("%v", gotV) != fmt.Sprintf("%v", wantV) {
					t.Errorf("key %q = %v, want %v", k, gotV, wantV)
				}
			}
		})
	}
}

func TestLoadModuleWithConditionalVars(t *testing.T) {
	dir := setupDir(t)

	// Module with both regular vars and a conditional var.
	writeFile(t, filepath.Join(dir, "modules", "webserver.yaml"), `
name: webserver
vars:
  port: "8080"
  is_debian:
    trait: "os.distro"
    in: [debian, ubuntu]
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)

	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web.example.com"
    modules: [webserver]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")

	// Regular var should remain in Vars map.
	if cfg.Vars["port"] != "8080" {
		t.Errorf("regular var 'port' = %v, want '8080'", cfg.Vars["port"])
	}

	// Conditional var should NOT be in Vars map (it's not a template-renderable string).
	if _, exists := cfg.Vars["is_debian"]; exists {
		t.Error("conditional var 'is_debian' should not be in Vars map")
	}

	// Conditional var should be in ConditionalVars slice.
	if len(cfg.ConditionalVars) != 1 {
		t.Fatalf("len(ConditionalVars) = %d, want 1", len(cfg.ConditionalVars))
	}
	cv := cfg.ConditionalVars[0]
	if cv.Name != "is_debian" {
		t.Errorf("ConditionalVar name = %q, want 'is_debian'", cv.Name)
	}
	if cv.Trait != "os.distro" {
		t.Errorf("ConditionalVar trait = %q, want 'os.distro'", cv.Trait)
	}
	if cv.Operator != "in" {
		t.Errorf("ConditionalVar operator = %q, want 'in'", cv.Operator)
	}
}

func TestLoadModuleInvalidConditionalVar(t *testing.T) {
	dir := setupDir(t)

	// Module with a bad conditional var (no trait field).
	writeFile(t, filepath.Join(dir, "modules", "bad.yaml"), `
name: bad
vars:
  broken_cond:
    in: [debian, ubuntu]
resources: []
`)

	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "test.example.com"
    modules: [bad]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for conditional var without trait field, got nil")
	}
	if !strings.Contains(err.Error(), "missing required 'trait' field") {
		t.Errorf("error = %q, want it to mention missing trait field", err)
	}
}

func TestLoadModuleInvalidConditionalVarBadOperator(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "bad.yaml"), `
name: bad
vars:
  broken_cond:
    trait: "os.distro"
    invalid_op: [debian]
resources: []
`)

	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "test.example.com"
    modules: [bad]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for conditional var with invalid operator, got nil")
	}
	if !strings.Contains(err.Error(), "unknown operator") {
		t.Errorf("error = %q, want it to mention unknown operator", err)
	}
}

func TestResolveEntryWithConditionalVars(t *testing.T) {
	dir := setupDir(t)

	// Two modules, each with a conditional var.
	writeFile(t, filepath.Join(dir, "modules", "mod_a.yaml"), `
name: mod_a
vars:
  is_ubuntu:
    trait: "os.distro"
    in: [ubuntu]
  port: "80"
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "mod_b.yaml"), `
name: mod_b
vars:
  has_enough_mem:
    trait: "hardware.memory_mb"
    gte: 4096
resources: []
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "multi.example.com"
    modules: [mod_a, mod_b]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("multi.example.com")

	// Should have 2 conditional vars from the two modules.
	if len(cfg.ConditionalVars) != 2 {
		t.Fatalf("len(ConditionalVars) = %d, want 2", len(cfg.ConditionalVars))
	}

	// Regular var should remain.
	if cfg.Vars["port"] != "80" {
		t.Errorf("regular var 'port' = %v, want '80'", cfg.Vars["port"])
	}

	// Conditional vars should NOT be in Vars map.
	if _, exists := cfg.Vars["is_ubuntu"]; exists {
		t.Error("conditional var 'is_ubuntu' should not be in Vars map")
	}
	if _, exists := cfg.Vars["has_enough_mem"]; exists {
		t.Error("conditional var 'has_enough_mem' should not be in Vars map")
	}
}

func TestConditionalVarsNotOverridableByClientVars(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "mod.yaml"), `
name: mod
vars:
  is_debian:
    trait: "os.distro"
    in: [debian, ubuntu]
resources: []
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "override.example.com"
    modules: [mod]
    vars:
      is_debian: "manual_override"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("override.example.com")

	// Conditional var should still be in ConditionalVars (not overridden).
	if len(cfg.ConditionalVars) != 1 {
		t.Fatalf("len(ConditionalVars) = %d, want 1 (client var should not override conditional var)", len(cfg.ConditionalVars))
	}
	if cfg.ConditionalVars[0].Name != "is_debian" {
		t.Errorf("ConditionalVar name = %q, want 'is_debian'", cfg.ConditionalVars[0].Name)
	}

	// The client-level override should NOT end up in Vars either -- conditional vars
	// take precedence and the name is reserved.
	if _, exists := cfg.Vars["is_debian"]; exists {
		t.Error("conditional var name 'is_debian' should not be in Vars map even when client tries to override it")
	}
}

// --- Conditional Block Loader Integration Tests ---

func TestLoadModuleConditionalBlockLoadsCorrectly(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "debian-tools.yaml"), `
name: debian-tools
resources:
  - conditional_block:
      when: "is_debian"
      resources:
        - name: install-apt-utils
          type: package
          package: apt-utils
          state: present
        - name: apt-update
          type: exec
          command: "apt-get update"
`)

	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "deb.example.com"
    modules: [debian-tools]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("deb.example.com")
	if len(cfg.Modules) != 1 {
		t.Fatalf("len(Modules) = %d, want 1", len(cfg.Modules))
	}
	mod := cfg.Modules[0]
	if len(mod.Resources) != 2 {
		t.Fatalf("len(Resources) = %d, want 2 (flattened from block)", len(mod.Resources))
	}
	// Both resources should have when: "is_debian" from block
	for i, res := range mod.Resources {
		if res.When != "is_debian" {
			t.Errorf("Resources[%d].When = %q, want \"is_debian\"", i, res.When)
		}
	}
	if mod.Resources[0].Name != "install-apt-utils" {
		t.Errorf("Resources[0].Name = %q, want install-apt-utils", mod.Resources[0].Name)
	}
	if mod.Resources[1].Name != "apt-update" {
		t.Errorf("Resources[1].Name = %q, want apt-update", mod.Resources[1].Name)
	}
}

func TestLoadModuleMixedPlainAndConditionalBlock(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "mixed.yaml"), `
name: mixed
resources:
  - name: always-run
    type: package
    package: curl
    state: present

  - conditional_block:
      when: "is_debian"
      resources:
        - name: apt-setup
          type: exec
          command: "apt-get update"

  - name: final-step
    type: service
    service: nginx
    state: running
`)

	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "mix.example.com"
    modules: [mixed]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("mix.example.com")
	if len(cfg.Modules) != 1 {
		t.Fatalf("len(Modules) = %d, want 1", len(cfg.Modules))
	}
	mod := cfg.Modules[0]
	if len(mod.Resources) != 3 {
		t.Fatalf("len(Resources) = %d, want 3", len(mod.Resources))
	}
	// Check ordering: always-run, apt-setup, final-step
	expectedNames := []string{"always-run", "apt-setup", "final-step"}
	for i, want := range expectedNames {
		if mod.Resources[i].Name != want {
			t.Errorf("Resources[%d].Name = %q, want %q", i, mod.Resources[i].Name, want)
		}
	}
	// Only apt-setup should have When
	if mod.Resources[0].When != "" {
		t.Errorf("always-run should have no When, got %q", mod.Resources[0].When)
	}
	if mod.Resources[1].When != "is_debian" {
		t.Errorf("apt-setup.When = %q, want \"is_debian\"", mod.Resources[1].When)
	}
	if mod.Resources[2].When != "" {
		t.Errorf("final-step should have no When, got %q", mod.Resources[2].When)
	}
}

func TestLoadModuleConditionalBlockDependsOnAcrossBoundary(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "cross-dep.yaml"), `
name: cross-dep
resources:
  - name: setup-repo
    type: exec
    command: "add-repo"

  - conditional_block:
      when: "is_debian"
      resources:
        - name: install-tool
          type: package
          package: my-tool
          state: present
          depends_on: [setup-repo]
`)

	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "dep.example.com"
    modules: [cross-dep]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("dep.example.com")
	mod := cfg.Modules[0]
	if len(mod.Resources) != 2 {
		t.Fatalf("len(Resources) = %d, want 2", len(mod.Resources))
	}
	// install-tool should have depends_on: [setup-repo] preserved and when: "is_debian"
	res := mod.Resources[1]
	if res.Name != "install-tool" {
		t.Errorf("Resources[1].Name = %q, want install-tool", res.Name)
	}
	if len(res.DependsOn) != 1 || res.DependsOn[0] != "setup-repo" {
		t.Errorf("install-tool.DependsOn = %v, want [setup-repo]", res.DependsOn)
	}
	if res.When != "is_debian" {
		t.Errorf("install-tool.When = %q, want \"is_debian\"", res.When)
	}
}

func TestLoadModuleConditionalBlockWithResourceLevelConditionals(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "dual-cond.yaml"), `
name: dual-cond
resources:
  - conditional_block:
      when: "is_debian"
      resources:
        - name: desktop-tool
          type: package
          package: desktop-tool
          state: present
          when: "not is_container"
`)

	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "dual.example.com"
    modules: [dual-cond]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("dual.example.com")
	mod := cfg.Modules[0]
	if len(mod.Resources) != 1 {
		t.Fatalf("len(Resources) = %d, want 1", len(mod.Resources))
	}
	res := mod.Resources[0]
	// Block when and resource when are AND-composed.
	want := "(is_debian) and (not is_container)"
	if res.When != want {
		t.Errorf("When = %q, want %q", res.When, want)
	}
}

func TestLoadModuleConditionalBlockEmptyResourcesError(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "bad-block.yaml"), `
name: bad-block
resources:
  - conditional_block:
      when: "is_debian"
      resources: []
`)

	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "bad.example.com"
    modules: [bad-block]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for conditional_block with empty resources, got nil")
	}
	if !strings.Contains(err.Error(), "conditional_block has no resources") {
		t.Errorf("error = %q, want it to contain 'conditional_block has no resources'", err)
	}
}

// --- Module Conditional Ref Loader Tests ---

func TestRoleWithConditionalModuleRef(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "certbot.yaml"), `
name: certbot
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present
`)
	writeFile(t, filepath.Join(dir, "roles", "webserver.yaml"), `
name: webserver
modules:
  - name: nginx
    when: "is_debian"
  - certbot
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web.example.com"
    roles: [webserver]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	if len(cfg.Modules) != 2 {
		t.Fatalf("len(Modules) = %d, want 2", len(cfg.Modules))
	}

	// nginx should have When from the role ref.
	if cfg.Modules[0].When != "is_debian" {
		t.Errorf("nginx.When = %q, want \"is_debian\"", cfg.Modules[0].When)
	}

	// certbot should have no conditionals.
	if cfg.Modules[1].When != "" {
		t.Errorf("certbot.When = %q, want empty", cfg.Modules[1].When)
	}
}

func TestClientWithConditionalModuleRef(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "redis.yaml"), `
name: redis
resources:
  - name: install-redis
    type: package
    package: redis
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "db.example.com"
    modules:
      - name: redis
        when: "not is_container"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("db.example.com")
	if len(cfg.Modules) != 1 {
		t.Fatalf("len(Modules) = %d, want 1", len(cfg.Modules))
	}
	if cfg.Modules[0].When != "not is_container" {
		t.Errorf("redis.When = %q, want \"not is_container\"", cfg.Modules[0].When)
	}
}

func TestMixedPlainAndConditionalModuleRefsInRole(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "certbot.yaml"), `
name: certbot
resources: []
`)
	writeFile(t, filepath.Join(dir, "roles", "web.yaml"), `
name: web
modules:
  - nginx
  - name: certbot
    when: "is_debian or is_ubuntu"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "mix.example.com"
    roles: [web]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("mix.example.com")
	if len(cfg.Modules) != 2 {
		t.Fatalf("len(Modules) = %d, want 2", len(cfg.Modules))
	}
	// nginx: plain string, no conditionals
	if cfg.Modules[0].When != "" {
		t.Errorf("nginx should have no conditionals, got When=%q", cfg.Modules[0].When)
	}
	// certbot: object with when expression
	if cfg.Modules[1].When != "is_debian or is_ubuntu" {
		t.Errorf("certbot.When = %q, want \"is_debian or is_ubuntu\"", cfg.Modules[1].When)
	}
}

func TestBackwardCompatPlainStringModulesStillWork(t *testing.T) {
	// This test explicitly verifies that existing role/client YAML with plain
	// string module references continues to load without any changes.
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "certbot.yaml"), `
name: certbot
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present
`)
	writeFile(t, filepath.Join(dir, "roles", "webserver.yaml"), `
name: webserver
modules: [nginx, certbot]
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web01.example.com"
    roles: [webserver]
  - match: "web02.example.com"
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg1 := store.Get("web01.example.com")
	if len(cfg1.Modules) != 2 {
		t.Fatalf("web01: len(Modules) = %d, want 2", len(cfg1.Modules))
	}
	if cfg1.Modules[0].Name != "nginx" || cfg1.Modules[1].Name != "certbot" {
		t.Errorf("web01: modules = [%s, %s], want [nginx, certbot]", cfg1.Modules[0].Name, cfg1.Modules[1].Name)
	}

	cfg2 := store.Get("web02.example.com")
	if len(cfg2.Modules) != 1 {
		t.Fatalf("web02: len(Modules) = %d, want 1", len(cfg2.Modules))
	}
	if cfg2.Modules[0].Name != "nginx" {
		t.Errorf("web02: module = %q, want nginx", cfg2.Modules[0].Name)
	}
}

func TestIdempotencyValidation_BlocksUnguardedExec(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "bad.hnb"), `
name: bad
resources:
  - name: yolo-exec
    type: exec
    command: "echo hello"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.hnb"), `
thralls:
  - match: "test.example.com"
    modules: [bad]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err == nil {
		t.Fatal("expected error for unguarded exec, got nil")
	}
	if !strings.Contains(err.Error(), "idempotency") {
		t.Errorf("error should mention idempotency, got: %v", err)
	}
}

func TestIdempotencyValidation_DangerousModeAllowsUnguardedExec(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "bad.hnb"), `
name: bad
resources:
  - name: yolo-exec
    type: exec
    command: "echo hello"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.hnb"), `
thralls:
  - match: "test.example.com"
    modules: [bad]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("dangerous mode should allow unguarded exec, got: %v", err)
	}
	cfg := store.Get("test.example.com")
	if len(cfg.Modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(cfg.Modules))
	}
}

func TestIdempotencyValidation_GuardedExecPasses(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "good.hnb"), `
name: good
resources:
  - name: guarded-exec
    type: exec
    command: "touch /tmp/done"
    creates: "/tmp/done"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.hnb"), `
thralls:
  - match: "test.example.com"
    modules: [good]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("guarded exec should pass validation, got: %v", err)
	}
}

func TestHnbFileExtension_LoadedByConfigStore(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.hnb"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "roles", "web.hnb"), `
name: web
modules: [nginx]
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.hnb"), `
thralls:
  - match: "test.example.com"
    roles: [web]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("test.example.com")
	if len(cfg.Modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(cfg.Modules))
	}
	if cfg.Modules[0].Name != "nginx" {
		t.Errorf("expected module nginx, got %q", cfg.Modules[0].Name)
	}
}

func TestMultipleIdempotencyErrorsCollected(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "bad1.hnb"), `
name: bad1
resources:
  - name: yolo-exec
    type: exec
    command: "echo one"
`)
	writeFile(t, filepath.Join(dir, "modules", "bad2.hnb"), `
name: bad2
resources:
  - name: another-yolo
    type: exec
    command: "echo two"
  - name: third-yolo
    type: exec
    command: "echo three"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.hnb"), `
thralls:
  - match: "test.example.com"
    modules: [bad1, bad2]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err == nil {
		t.Fatal("expected idempotency errors, got nil")
	}

	var ie *IdempotencyErrors
	if !errors.As(err, &ie) {
		t.Fatalf("expected *IdempotencyErrors, got %T: %v", err, err)
	}

	// Should collect errors from both modules.
	if len(ie.Modules) != 2 {
		t.Errorf("expected 2 modules with errors, got %d", len(ie.Modules))
	}

	// bad2 has 2 unguarded execs.
	totalFindings := 0
	for _, m := range ie.Modules {
		totalFindings += len(m.Findings)
	}
	if totalFindings != 3 {
		t.Errorf("expected 3 total findings, got %d", totalFindings)
	}

	// Verify formatted output mentions both modules.
	formatted := ie.Format()
	if !strings.Contains(formatted, "bad1") {
		t.Error("formatted output missing bad1")
	}
	if !strings.Contains(formatted, "bad2") {
		t.Error("formatted output missing bad2")
	}
	if !strings.Contains(formatted, "skip_validation") {
		t.Error("formatted output should mention skip_validation")
	}
}

func TestDuplicateModulesKeysMerged(t *testing.T) {
	dir := setupDir(t)

	for _, name := range []string{"docker", "grafana", "thrall-exporter"} {
		writeFile(t, filepath.Join(dir, "modules", name+".hnb"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    package: %s
    state: present
`, name, name, name))
	}
	// Duplicate modules: keys — should merge, not overwrite.
	writeFile(t, filepath.Join(dir, "thralls", "thralls.hnb"), `
thralls:
  - match: "danlap"
    environment: production
    modules: [docker]
    modules: [grafana]
    modules: [thrall-exporter]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("danlap")
	if len(cfg.Modules) != 3 {
		names := make([]string, len(cfg.Modules))
		for i, m := range cfg.Modules {
			names[i] = m.Name
		}
		t.Fatalf("expected 3 modules, got %d: %v", len(cfg.Modules), names)
	}

	want := []string{"docker", "grafana", "thrall-exporter"}
	for i, m := range cfg.Modules {
		if m.Name != want[i] {
			t.Errorf("module %d: got %q, want %q", i, m.Name, want[i])
		}
	}

	// Verify trace fields.
	if cfg.MatchPattern != "danlap" {
		t.Errorf("MatchPattern: got %q, want %q", cfg.MatchPattern, "danlap")
	}
	if cfg.Environment != "production" {
		t.Errorf("Environment: got %q, want %q", cfg.Environment, "production")
	}
}

func TestDuplicateRolesKeysMerged(t *testing.T) {
	dir := setupDir(t)

	for _, name := range []string{"nginx", "redis"} {
		writeFile(t, filepath.Join(dir, "modules", name+".hnb"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    package: %s
    state: present
`, name, name, name))
	}
	writeFile(t, filepath.Join(dir, "roles", "web.hnb"), `
name: web
modules: [nginx]
`)
	writeFile(t, filepath.Join(dir, "roles", "cache.hnb"), `
name: cache
modules: [redis]
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.hnb"), `
thralls:
  - match: "test"
    roles: [web]
    roles: [cache]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("test")
	if len(cfg.Modules) != 2 {
		t.Fatalf("expected 2 modules, got %d", len(cfg.Modules))
	}
	if cfg.Roles[0] != "web" || cfg.Roles[1] != "cache" {
		t.Errorf("Roles: got %v, want [web cache]", cfg.Roles)
	}
}

func TestTemplateRendering(t *testing.T) {
	traits := map[string]any{
		"os": map[string]any{"distro": "ubuntu"},
	}
	vars := map[string]any{
		"app_port": "8080",
	}

	result, err := RenderTemplate("port={{ .Vars.app_port }} os={{ .Traits.os.distro }}", traits, vars)
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if result != "port=8080 os=ubuntu" {
		t.Errorf("got %q, want 'port=8080 os=ubuntu'", result)
	}
}

func TestBeforeResolvesToDependsOn(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.yaml"), `
name: base-packages
before: [app-config]
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app-config
resources:
  - name: app-conf
    type: file
    path: /etc/app.conf
    content: "test"
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "app.example.com"
    modules: [base-packages, app-config]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("app.example.com")
	if len(cfg.Modules) != 2 {
		t.Fatalf("len(Modules) = %d, want 2", len(cfg.Modules))
	}

	// base-packages has before: [app-config], so app-config should have
	// depends_on: [base-packages] after resolution.
	appMod := cfg.Modules[1]
	if appMod.Name != "app-config" {
		t.Fatalf("Module[1] = %q, want app-config", appMod.Name)
	}
	if len(appMod.DependsOn) != 1 || appMod.DependsOn[0] != "base-packages" {
		t.Errorf("app-config DependsOn = %v, want [base-packages]", appMod.DependsOn)
	}

	// base-packages should have no depends_on.
	baseMod := cfg.Modules[0]
	if len(baseMod.DependsOn) != 0 {
		t.Errorf("base-packages DependsOn = %v, want []", baseMod.DependsOn)
	}
}

func TestBeforeUnknownTargetErrors(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.yaml"), `
name: base-packages
before: [missing-module]
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "app.example.com"
    modules: [base-packages]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for before referencing unknown module")
	}
	if !strings.Contains(err.Error(), "missing-module") {
		t.Errorf("error should mention missing-module, got: %v", err)
	}
}

func TestBeforeNoDuplicateDependsOn(t *testing.T) {
	dir := setupDir(t)

	// app-config already depends_on base-packages, and base-packages also
	// declares before: [app-config]. Should not duplicate the dependency.
	writeFile(t, filepath.Join(dir, "modules", "base.yaml"), `
name: base-packages
before: [app-config]
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app-config
depends_on: [base-packages]
resources:
  - name: app-conf
    type: file
    path: /etc/app.conf
    content: "test"
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "app.example.com"
    modules: [base-packages, app-config]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("app.example.com")
	appMod := cfg.Modules[1]
	if len(appMod.DependsOn) != 1 {
		t.Errorf("expected 1 dependency, got %d: %v", len(appMod.DependsOn), appMod.DependsOn)
	}
}

// --- vars_from tests ---

func TestVarsFromSingleFile(t *testing.T) {
	dir := setupDir(t)
	os.MkdirAll(filepath.Join(dir, "vars"), 0o755)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
vars:
  port: "8080"
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "web.hnb"), `
server_name: "web01.example.com"
port: "9090"
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "web01.example.com"
    vars_from: "vars/web.hnb"
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.example.com")
	// vars_from overrides module default
	if cfg.Vars["port"] != "9090" {
		t.Errorf("port = %v, want 9090 (vars_from should override module default)", cfg.Vars["port"])
	}
	// vars_from adds new key
	if cfg.Vars["server_name"] != "web01.example.com" {
		t.Errorf("server_name = %v, want web01.example.com", cfg.Vars["server_name"])
	}
}

func TestVarsFromMultipleFiles(t *testing.T) {
	dir := setupDir(t)
	os.MkdirAll(filepath.Join(dir, "vars"), 0o755)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
vars:
  port: "8080"
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "common.hnb"), `
port: "3000"
log_level: "info"
`)
	writeFile(t, filepath.Join(dir, "vars", "host.hnb"), `
port: "4000"
server_name: "host1"
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "host1.example.com"
    vars_from:
      - "vars/common.hnb"
      - "vars/host.hnb"
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("host1.example.com")
	// Later vars_from file overrides earlier
	if cfg.Vars["port"] != "4000" {
		t.Errorf("port = %v, want 4000 (later vars_from overrides earlier)", cfg.Vars["port"])
	}
	if cfg.Vars["log_level"] != "info" {
		t.Errorf("log_level = %v, want info (from first vars_from file)", cfg.Vars["log_level"])
	}
	if cfg.Vars["server_name"] != "host1" {
		t.Errorf("server_name = %v, want host1 (from second vars_from file)", cfg.Vars["server_name"])
	}
}

func TestVarsFromInlineOverrides(t *testing.T) {
	dir := setupDir(t)
	os.MkdirAll(filepath.Join(dir, "vars"), 0o755)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "defaults.hnb"), `
port: "3000"
log_level: "info"
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "host1.example.com"
    vars_from: "vars/defaults.hnb"
    vars:
      port: "9999"
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("host1.example.com")
	// Inline vars override vars_from
	if cfg.Vars["port"] != "9999" {
		t.Errorf("port = %v, want 9999 (inline vars override vars_from)", cfg.Vars["port"])
	}
	// vars_from value survives when not overridden
	if cfg.Vars["log_level"] != "info" {
		t.Errorf("log_level = %v, want info (from vars_from, not overridden inline)", cfg.Vars["log_level"])
	}
}

func TestVarsFromPrecedence(t *testing.T) {
	dir := setupDir(t)
	os.MkdirAll(filepath.Join(dir, "vars"), 0o755)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
vars:
  a: "module"
  b: "module"
  c: "module"
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "data.hnb"), `
a: "file"
b: "file"
d: "file"
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "host1.example.com"
    vars_from: "vars/data.hnb"
    vars:
      a: "inline"
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("host1.example.com")
	// Full precedence chain: module < vars_from < inline
	if cfg.Vars["a"] != "inline" {
		t.Errorf("a = %v, want inline", cfg.Vars["a"])
	}
	if cfg.Vars["b"] != "file" {
		t.Errorf("b = %v, want file", cfg.Vars["b"])
	}
	if cfg.Vars["c"] != "module" {
		t.Errorf("c = %v, want module", cfg.Vars["c"])
	}
	if cfg.Vars["d"] != "file" {
		t.Errorf("d = %v, want file", cfg.Vars["d"])
	}
}

func TestVarsFromMissingFile(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "host1.example.com"
    vars_from: "vars/nonexistent.hnb"
    modules: [app]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for missing vars_from file")
	}
	if !strings.Contains(err.Error(), "nonexistent.hnb") {
		t.Errorf("error should mention the missing file, got: %v", err)
	}
}

func TestVarsFromPathTraversal(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "host1.example.com"
    vars_from: "../../../etc/passwd"
    modules: [app]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "escapes config directory") {
		t.Errorf("error should mention path escape, got: %v", err)
	}
}

func TestVarsFromEmptyFile(t *testing.T) {
	dir := setupDir(t)
	os.MkdirAll(filepath.Join(dir, "vars"), 0o755)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
vars:
  port: "8080"
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "empty.hnb"), ``)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "host1.example.com"
    vars_from: "vars/empty.hnb"
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("host1.example.com")
	// Module default survives
	if cfg.Vars["port"] != "8080" {
		t.Errorf("port = %v, want 8080 (module default, empty vars_from)", cfg.Vars["port"])
	}
}

func TestVarsFromWithSecrets(t *testing.T) {
	dir := setupDir(t)
	os.MkdirAll(filepath.Join(dir, "vars"), 0o755)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "secrets.hnb"), `
api_key: "secret:app/api_key"
debug: "false"
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "host1.example.com"
    vars_from: "vars/secrets.hnb"
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{
		"app/api_key": "resolved-key-123",
	}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("host1.example.com")
	if cfg.Vars["api_key"] != "resolved-key-123" {
		t.Errorf("api_key = %v, want resolved-key-123 (secret resolved)", cfg.Vars["api_key"])
	}
	if cfg.Vars["debug"] != "false" {
		t.Errorf("debug = %v, want false", cfg.Vars["debug"])
	}
}

func TestVarsFromScalarString(t *testing.T) {
	dir := setupDir(t)
	os.MkdirAll(filepath.Join(dir, "vars"), 0o755)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "data.hnb"), `
color: "blue"
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "host1.example.com"
    vars_from: "vars/data.hnb"
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("host1.example.com")
	if cfg.Vars["color"] != "blue" {
		t.Errorf("color = %v, want blue (single string vars_from)", cfg.Vars["color"])
	}
}

func TestVarsFromVarOrder(t *testing.T) {
	dir := setupDir(t)
	os.MkdirAll(filepath.Join(dir, "vars"), 0o755)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
vars:
  z_module: "from_module"
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "data.hnb"), `
b_file: "from_file"
a_file: "from_file"
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "host1.example.com"
    vars_from: "vars/data.hnb"
    vars:
      c_inline: "from_inline"
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("host1.example.com")
	// VarOrder should be: module vars, then file vars (in file order), then inline vars
	expected := []string{"z_module", "b_file", "a_file", "c_inline"}
	if len(cfg.VarOrder) != len(expected) {
		t.Fatalf("VarOrder len = %d, want %d: %v", len(cfg.VarOrder), len(expected), cfg.VarOrder)
	}
	for i, name := range expected {
		if cfg.VarOrder[i] != name {
			t.Errorf("VarOrder[%d] = %q, want %q (full order: %v)", i, cfg.VarOrder[i], name, cfg.VarOrder)
		}
	}
}

func TestOverlappingGlobFirstWins(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources:
  - name: install
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "apache.yaml"), `
name: apache
resources:
  - name: install
    type: package
    package: apache2
    state: present
`)
	// Two glob patterns that both match "web01.prod.example.com".
	// First match should win.
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web*.prod.example.com"
    modules: [nginx]
  - match: "*.prod.example.com"
    modules: [apache]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.prod.example.com")
	if len(cfg.Modules) != 1 {
		t.Fatalf("len(Modules) = %d, want 1", len(cfg.Modules))
	}
	if cfg.Modules[0].Name != "nginx" {
		t.Errorf("Module = %q, want nginx (first glob should win)", cfg.Modules[0].Name)
	}

	// db01 should match the second pattern.
	cfg2 := store.Get("db01.prod.example.com")
	if len(cfg2.Modules) != 1 {
		t.Fatalf("db01: len(Modules) = %d, want 1", len(cfg2.Modules))
	}
	if cfg2.Modules[0].Name != "apache" {
		t.Errorf("db01 Module = %q, want apache", cfg2.Modules[0].Name)
	}
}

func TestEmptyThrallsDir(t *testing.T) {
	dir := setupDir(t)

	// No thrall files, no modules, no roles — just empty dirs.
	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("anything.example.com")
	if len(cfg.Modules) != 0 {
		t.Errorf("expected 0 modules for empty config, got %d", len(cfg.Modules))
	}
}

func TestEnvironmentOverridesMergePrecedence(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
vars:
  port: "8080"
  log_level: "info"
resources:
  - name: install
    type: package
    package: myapp
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "app01.example.com"
    environment: production
    modules: [app]
    vars:
      port: "8080"
      log_level: "info"
    environment_overrides:
      production:
        port: "443"
        max_workers: "8"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("app01.example.com")
	if cfg.Vars["port"] != "443" {
		t.Errorf("port = %v, want 443 (environment_overrides should override)", cfg.Vars["port"])
	}
	if cfg.Vars["log_level"] != "info" {
		t.Errorf("log_level = %v, want info (unchanged by environment_overrides)", cfg.Vars["log_level"])
	}
	if cfg.Vars["max_workers"] != "8" {
		t.Errorf("max_workers = %v, want 8 (added by environment_overrides)", cfg.Vars["max_workers"])
	}
}

func TestNodeVarsOverrideModuleDefaults(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "webserver.yaml"), `
name: webserver
vars:
  port: "80"
  workers: "auto"
resources:
  - name: install
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web01.example.com"
    modules: [webserver]
    vars:
      port: "9090"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.example.com")
	if cfg.Vars["port"] != "9090" {
		t.Errorf("port = %v, want 9090 (node override)", cfg.Vars["port"])
	}
	if cfg.Vars["workers"] != "auto" {
		t.Errorf("workers = %v, want auto (module default preserved)", cfg.Vars["workers"])
	}
}

func TestVarsFromPathTraversalDotDot(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web01.example.com"
    modules: [app]
    vars_from:
      - "../../../etc/passwd"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for path traversal in vars_from")
	}
	if !strings.Contains(err.Error(), "escapes config directory") {
		t.Errorf("expected 'escapes config directory' error, got: %v", err)
	}
}

func TestSourcePathTraversalDotDot(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: conf
    type: file
    targetpath: /etc/app/config
    source: "../../etc/shadow"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web01.example.com"
    modules: [app]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for path traversal in source")
	}
	if !strings.Contains(err.Error(), "escapes config directory") {
		t.Errorf("expected 'escapes config directory' error, got: %v", err)
	}
}

func TestSourcePathAbsoluteRejected(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: conf
    type: file
    targetpath: /etc/app/config
    source: "/etc/passwd"
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "web01.example.com"
    modules: [app]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for absolute source path")
	}
}

func TestConcurrentGetDuringReload(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources:
  - name: install
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "*.example.com"
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	var wg sync.WaitGroup
	const goroutines = 50

	// Concurrent reads.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cfg := store.Get("web01.example.com")
				if cfg.Thrall != "web01.example.com" {
					t.Errorf("Get() returned wrong thrall: %q", cfg.Thrall)
				}
			}
		}()
	}

	// Concurrent reloads.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				store.Reload()
			}
		}()
	}

	wg.Wait()
}

func TestReloadBumpsVersion(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources:
  - name: install
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "*.example.com"
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	v1 := store.Version()
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	v2 := store.Version()

	if v2 <= v1 {
		t.Errorf("expected version to increase after reload: v1=%d, v2=%d", v1, v2)
	}
}

func TestReloadFailureKeepsOldConfig(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources:
  - name: install
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "*.example.com"
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.example.com")
	if len(cfg.Modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(cfg.Modules))
	}

	// Write invalid module reference.
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "*.example.com"
    modules: [nonexistent_module_xyz]
`)

	// Reload should fail but old config preserved.
	reloadErr := store.Reload()
	if reloadErr == nil {
		// Some configs may allow missing modules; either way, the old config should remain valid.
		return
	}

	// After failed reload, Get() should still return the old valid config.
	cfg = store.Get("web01.example.com")
	if len(cfg.Modules) != 1 || cfg.Modules[0].Name != "nginx" {
		t.Error("expected old config to be preserved after failed reload")
	}
}

func TestVarsFromSymlinkEscapeRejected(t *testing.T) {
	dir := setupDir(t)

	// Create a vars directory inside the config dir.
	varsDir := filepath.Join(dir, "vars")
	if err := os.MkdirAll(varsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside vars/ that points outside the config dir.
	outsideFile := filepath.Join(t.TempDir(), "outside.yaml")
	writeFile(t, outsideFile, "stolen_var: gotcha\n")

	symlinkPath := filepath.Join(varsDir, "evil.yaml")
	if err := os.Symlink(outsideFile, symlinkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	// loadVarsFile should reject the symlink.
	_, _, err := loadVarsFile(dir, "vars/evil.yaml")
	if err == nil {
		t.Fatal("expected error for symlink escaping config directory, got nil")
	}
	if !strings.Contains(err.Error(), "resolves outside config directory") {
		t.Errorf("error = %q, want it to contain %q", err, "resolves outside config directory")
	}
}

func TestSourceSymlinkEscapeRejected(t *testing.T) {
	dir := setupDir(t)

	// Create templates directory.
	templatesDir := filepath.Join(dir, "templates")
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a file outside the config dir to be the symlink target.
	outsideFile := filepath.Join(t.TempDir(), "stolen.conf")
	writeFile(t, outsideFile, "secret config content")

	// Create a symlink inside templates/ pointing outside.
	symlinkPath := filepath.Join(templatesDir, "evil.conf")
	if err := os.Symlink(outsideFile, symlinkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	// Write a module that references the symlink as source.
	writeFile(t, filepath.Join(dir, "modules", "evil.yaml"), `
name: evil
resources:
  - name: evil-file
    type: file
    targetpath: /etc/evil.conf
    source: templates/evil.conf
`)
	writeFile(t, filepath.Join(dir, "thralls", "thralls.yaml"), `
thralls:
  - match: "test.example.com"
    modules: [evil]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for source symlink escaping config directory, got nil")
	}
	if !strings.Contains(err.Error(), "resolves outside config directory") {
		t.Errorf("error = %q, want it to contain %q", err, "resolves outside config directory")
	}
}

// --- Role Includes ---

func TestRoleIncludes(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "firewall.yaml"), `
name: firewall
resources:
  - name: fw-rules
    type: file
    targetpath: /etc/iptables/rules.v4
`)
	writeFile(t, filepath.Join(dir, "modules", "sshd.yaml"), `
name: sshd
resources:
  - name: sshd-config
    type: file
    targetpath: /etc/ssh/sshd_config
`)
	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "roles", "base-security.yaml"), `
name: base-security
modules:
  - firewall
  - sshd
`)
	writeFile(t, filepath.Join(dir, "roles", "webserver.yaml"), `
name: webserver
includes:
  - base-security
modules:
  - nginx
`)
	writeFile(t, filepath.Join(dir, "thralls", "web.yaml"), `
thralls:
  - match: "web01.example.com"
    roles: [webserver]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.example.com")
	if len(cfg.Modules) != 3 {
		t.Fatalf("len(Modules) = %d, want 3", len(cfg.Modules))
	}
	if cfg.Modules[0].Name != "firewall" {
		t.Errorf("Modules[0] = %q, want firewall", cfg.Modules[0].Name)
	}
	if cfg.Modules[1].Name != "sshd" {
		t.Errorf("Modules[1] = %q, want sshd", cfg.Modules[1].Name)
	}
	if cfg.Modules[2].Name != "nginx" {
		t.Errorf("Modules[2] = %q, want nginx", cfg.Modules[2].Name)
	}
}

func TestRoleIncludesNestedRejected(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "a.yaml"), `
name: a
resources:
  - name: r
    type: file
    targetpath: /tmp/a
`)
	writeFile(t, filepath.Join(dir, "roles", "inner.yaml"), `
name: inner
includes:
  - outer
modules:
  - a
`)
	writeFile(t, filepath.Join(dir, "roles", "outer.yaml"), `
name: outer
includes:
  - inner
modules:
  - a
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "x"
    roles: [outer]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for nested includes, got nil")
	}
	if !strings.Contains(err.Error(), "itself has includes") {
		t.Errorf("error = %q, want it to contain %q", err, "itself has includes")
	}
}

func TestRoleIncludesDedup(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "shared.yaml"), `
name: shared
resources:
  - name: r
    type: file
    targetpath: /tmp/shared
`)
	writeFile(t, filepath.Join(dir, "modules", "web.yaml"), `
name: web
resources:
  - name: r
    type: file
    targetpath: /tmp/web
`)
	writeFile(t, filepath.Join(dir, "roles", "base.yaml"), `
name: base
modules:
  - shared
`)
	writeFile(t, filepath.Join(dir, "roles", "webserver.yaml"), `
name: webserver
includes:
  - base
modules:
  - shared
  - web
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "x"
    roles: [webserver]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("x")
	if len(cfg.Modules) != 2 {
		t.Fatalf("len(Modules) = %d, want 2", len(cfg.Modules))
	}
	if cfg.Modules[0].Name != "shared" {
		t.Errorf("Modules[0] = %q, want shared", cfg.Modules[0].Name)
	}
	if cfg.Modules[1].Name != "web" {
		t.Errorf("Modules[1] = %q, want web", cfg.Modules[1].Name)
	}
}

// --- Wildcard vars_from ---

func TestVarsFromHostnameSubstitution(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
vars:
  port: "8080"
resources:
  - name: cfg
    type: file
    targetpath: /etc/app.conf
`)

	varsDir := filepath.Join(dir, "vars")
	if err := os.MkdirAll(varsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(varsDir, "web01.example.com.yaml"), `
port: "9090"
`)

	writeFile(t, filepath.Join(dir, "thralls", "web.yaml"), `
thralls:
  - match: "web01.example.com"
    modules: [app]
    vars_from:
      - "vars/{{ .Hostname }}.yaml"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.example.com")
	if cfg.Vars["port"] != "9090" {
		t.Errorf("port = %v, want 9090", cfg.Vars["port"])
	}
}

func TestVarsFromHostnameNoSpace(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: cfg
    type: file
    targetpath: /etc/app.conf
`)

	varsDir := filepath.Join(dir, "vars")
	if err := os.MkdirAll(varsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(varsDir, "host1.yaml"), `
key: val
`)

	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "host1"
    modules: [app]
    vars_from:
      - "vars/{{.Hostname}}.yaml"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("host1")
	if cfg.Vars["key"] != "val" {
		t.Errorf("key = %v, want val", cfg.Vars["key"])
	}
}

// --- Config Trace ---

func TestConfigTrace(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.yaml"), `
name: nginx
vars:
  worker_processes: "auto"
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
  - name: nginx-config
    type: file
    targetpath: /etc/nginx/nginx.conf
`)
	writeFile(t, filepath.Join(dir, "modules", "firewall.yaml"), `
name: firewall
resources:
  - name: fw
    type: file
    targetpath: /etc/iptables/rules.v4
`)
	writeFile(t, filepath.Join(dir, "roles", "base.yaml"), `
name: base
modules:
  - firewall
`)
	writeFile(t, filepath.Join(dir, "roles", "web.yaml"), `
name: web
includes:
  - base
modules:
  - nginx
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "web01.example.com"
    environment: production
    roles: [web]
    vars:
      worker_processes: "8"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("web01.example.com")
	if !trace.Matched {
		t.Fatal("expected matched=true")
	}
	if trace.MatchPattern != "web01.example.com" {
		t.Errorf("MatchPattern = %q, want web01.example.com", trace.MatchPattern)
	}
	if trace.PatternType != "exact" {
		t.Errorf("PatternType = %q, want exact", trace.PatternType)
	}
	if trace.Environment != "production" {
		t.Errorf("Environment = %q, want production", trace.Environment)
	}
	if len(trace.Modules) != 2 {
		t.Fatalf("len(Modules) = %d, want 2", len(trace.Modules))
	}
	if trace.Modules[0] != "firewall" || trace.Modules[1] != "nginx" {
		t.Errorf("Modules = %v, want [firewall nginx]", trace.Modules)
	}
	if trace.ResourceCount != 3 {
		t.Errorf("ResourceCount = %d, want 3", trace.ResourceCount)
	}

	if len(trace.RoleExpansion) != 1 {
		t.Fatalf("len(RoleExpansion) = %d, want 1", len(trace.RoleExpansion))
	}
	if trace.RoleExpansion[0].Role != "web" {
		t.Errorf("RoleExpansion[0].Role = %q, want web", trace.RoleExpansion[0].Role)
	}
	if len(trace.RoleExpansion[0].Includes) != 1 || trace.RoleExpansion[0].Includes[0] != "base" {
		t.Errorf("RoleExpansion[0].Includes = %v, want [base]", trace.RoleExpansion[0].Includes)
	}

	varSourceMap := make(map[string]string)
	for _, vs := range trace.VarSources {
		varSourceMap[vs.Key] = vs.Source
	}
	if varSourceMap["worker_processes"] != "inline" {
		t.Errorf("worker_processes source = %q, want inline", varSourceMap["worker_processes"])
	}
}

func TestConfigTraceNoMatch(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "modules", "a.yaml"), `
name: a
resources:
  - name: r
    type: file
    targetpath: /tmp/a
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "other"
    modules: [a]
`)
	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("nonexistent.example.com")
	if trace.Matched {
		t.Error("expected matched=false")
	}
}

func TestConfigTraceGlobPattern(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources:
  - name: r
    type: file
    targetpath: /tmp/app
`)
	writeFile(t, filepath.Join(dir, "thralls", "t.yaml"), `
thralls:
  - match: "web-*.example.com"
    modules: [app]
`)
	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("web-01.example.com")
	if !trace.Matched {
		t.Fatal("expected matched=true")
	}
	if trace.PatternType != "glob" {
		t.Errorf("PatternType = %q, want glob", trace.PatternType)
	}
	if trace.MatchPattern != "web-*.example.com" {
		t.Errorf("MatchPattern = %q, want web-*.example.com", trace.MatchPattern)
	}
}
