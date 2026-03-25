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

// setupDir creates a config directory with modules/ subdir and a default
// subdirectory for envoy entries.
func setupDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "envoy-configs")
	for _, sub := range []string{"default", "modules"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeRoles writes a roles.vgo file at the base dir from individual role
// YAML snippets. Each snippet is a single role definition (name: ..., modules: ...).
// They are wrapped in a top-level "roles:" list.
func writeRoles(t *testing.T, dir string, roleSnippets ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("roles:\n")
	for _, snippet := range roleSnippets {
		for _, line := range strings.Split(strings.TrimSpace(snippet), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	writeFile(t, filepath.Join(dir, "roles.vgo"), b.String())
}

// --- Tests ---

func TestLoadModuleAndResolve(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)

	writeFile(t, filepath.Join(dir, "default", "webservers.vgo"), `
envoys:
  - match: "test.example.com"
    environment: production
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("test.example.com")
	if cfg.Envoy != "test.example.com" {
		t.Errorf("Envoy = %q, want test.example.com", cfg.Envoy)
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "certbot.vgo"), `
name: certbot
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present
`)
	writeRoles(t, dir, `
- name: webserver
  modules: [nginx, certbot]
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
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
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: config
    type: file
    target_path: /etc/app/db.conf
    content: "secret:myapp/db/config"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web.example.com"
    environment: production
    modules: [app]
    vars:
      db_dsn: "secret:vigo/db/dsn"
`)

	mp := &mockProvider{secrets: map[string]string{
		"vigo/db/dsn":   "/srv/vigo/db/vigo.db",
		"myapp/db/config": "host=db port=5432 user=myapp",
	}}
	store, err := NewConfigStore(dir, mp, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	if cfg.Vars["db_dsn"] != "/srv/vigo/db/vigo.db" {
		t.Errorf("db_dsn = %q, want resolved DSN", cfg.Vars["db_dsn"])
	}
}

func TestKnownSecretEnforcement(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "bad.example.com"
    environment: production
    modules: [app]
    vars:
      dsn: "plaintext-vigo.db"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for known-secret field without secret: prefix, got nil")
	}
}

func TestHardFailUnresolvable(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: config-file
    type: file
    path: /etc/app/config
    content: "secret:app/config/content"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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
		{"local not found", "local", fmt.Errorf("secret %q not found in /srv/vigo/secrets", "x"), "vigogo secrets set"},
		{"local wrong key", "local", fmt.Errorf("decrypting: message authentication failed (wrong master key?)"), "re-encrypt"},
		{"local too short", "local", fmt.Errorf("ciphertext too short"), "plaintext from a previous"},
		// Isopass backend hints.
		{"isopass not found", "isopass", fmt.Errorf("secret %q not found in isopass", "x"), "isopass set"},
		{"isopass unreachable", "isopass", fmt.Errorf("isopass unreachable at https://x"), "VIGO_ISOPASS_URL"},
		{"isopass auth failed", "isopass", fmt.Errorf("isopass authentication failed"), "VIGO_ISOPASS_TOKEN"},
		// Backend-agnostic.
		{"plaintext value", "", fmt.Errorf("must use \"secret:\" prefix"), "secret:<path>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se := &SecretError{Match: "test", Attr: "x", Secret: "vigo/db/dsn", Backend: tt.backend, Err: tt.err}
			if !strings.Contains(se.Hint(), tt.want) {
				t.Errorf("hint = %q, want substring %q", se.Hint(), tt.want)
			}
		})
	}
}

func TestPatternMatching(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "explicit.vgo"), `
name: explicit
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "pattern.vgo"), `
name: pattern
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "postgres.vgo"), `
name: postgres
resources: []
`)
	// Two separate client files
	writeFile(t, filepath.Join(dir, "default", "webservers.vgo"), `
envoys:
  - match: "web-*"
    environment: production
    modules: [nginx]
`)
	writeFile(t, filepath.Join(dir, "default", "databases.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

func TestConfigReload(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources: []
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// Initially unconfigured.
	if cfg := store.Get("new.example.com"); cfg.Environment != "" {
		t.Fatalf("expected empty environment before reload, got %q", cfg.Environment)
	}

	// Write a new envoy file and reload.
	writeFile(t, filepath.Join(dir, "default", "new-hosts.vgo"), `
envoys:
  - match: "new.example.com"
    environment: staging
    modules: [app]
`)

	if err := store.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	cfg := store.Get("new.example.com")
	if cfg.Environment != "staging" {
		t.Fatalf("expected environment=staging after reload, got %q", cfg.Environment)
	}
}

func TestBadConfigKeepsOld(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "good.example.com"
    environment: production
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	if cfg := store.Get("good.example.com"); cfg == nil {
		t.Fatal("expected good config")
	}

	// Write invalid YAML and reload.
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  invalid yaml: [[[
`)

	err = store.Reload()
	if err == nil {
		t.Fatal("expected reload error for invalid YAML")
	}

	// Good config should still be there.
	if cfg := store.Get("good.example.com"); cfg.Environment != "production" {
		t.Fatal("bad YAML replaced good config")
	}
}

func TestUnknownModuleError(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeRoles(t, dir, `
- name: bad
  modules: [nonexistent]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for role referencing unknown module, got nil")
	}
}

func TestDedupModules(t *testing.T) {
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: config
    type: file
    target_path: /etc/app/config
    content: "secret:myapp/db/config"
`)
	writeFile(t, filepath.Join(dir, "modules", "other.vgo"), `
name: other
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	patterns := store.EnvoysReferencingSecret("myapp/db/config")
	if len(patterns) != 1 || patterns[0] != "web-*" {
		t.Errorf("expected [web-*], got %v", patterns)
	}

	patterns = store.EnvoysReferencingSecret("nonexistent")
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
vars:
  worker_processes: "auto"
resources:
  - name: nginx-conf
    type: file
    target_path: /etc/nginx/nginx.conf
    source: templates/nginx.conf.tmpl
    owner: root
    mode: "0644"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "bad.vgo"), `
name: bad
resources:
  - name: both-set
    type: file
    target_path: /etc/test
    source: templates/test.tmpl
    content: "inline content"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "bad.vgo"), `
name: bad
resources:
  - name: missing-source
    type: file
    target_path: /etc/test
    source: templates/nonexistent.tmpl
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
vars:
  port: "80"
resources:
  - name: nginx-site
    type: file
    target_path: /etc/nginx/sites-enabled/default
    source: templates/nginx/sites/default.conf.tmpl
    mode: "0644"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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
	writeFile(t, filepath.Join(dir, "modules", "webserver.vgo"), `
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

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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
	writeFile(t, filepath.Join(dir, "modules", "bad.vgo"), `
name: bad
vars:
  broken_cond:
    in: [debian, ubuntu]
resources: []
`)

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "bad.vgo"), `
name: bad
vars:
  broken_cond:
    trait: "os.distro"
    invalid_op: [debian]
resources: []
`)

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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
	writeFile(t, filepath.Join(dir, "modules", "mod_a.vgo"), `
name: mod_a
vars:
  is_ubuntu:
    trait: "os.distro"
    in: [ubuntu]
  port: "80"
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "mod_b.vgo"), `
name: mod_b
vars:
  has_enough_mem:
    trait: "hardware.memory_mb"
    gte: 4096
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "mod.vgo"), `
name: mod
vars:
  is_debian:
    trait: "os.distro"
    in: [debian, ubuntu]
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "debian-tools.vgo"), `
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

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "mixed.vgo"), `
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

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "cross-dep.vgo"), `
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

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "dual-cond.vgo"), `
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

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "bad-block.vgo"), `
name: bad-block
resources:
  - conditional_block:
      when: "is_debian"
      resources: []
`)

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "certbot.vgo"), `
name: certbot
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present
`)
	writeRoles(t, dir, `
- name: webserver
  modules:
    - name: nginx
      when: "is_debian"
    - certbot
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "redis.vgo"), `
name: redis
resources:
  - name: install-redis
    type: package
    package: redis
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "certbot.vgo"), `
name: certbot
resources: []
`)
	writeRoles(t, dir, `
- name: web
  modules:
    - nginx
    - name: certbot
      when: "is_debian or is_ubuntu"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

func TestConditionalRoleRef(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "sshd.vgo"), `
name: sshd
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "iis.vgo"), `
name: iis
resources: []
`)
	writeRoles(t, dir, `
- name: linux-web
  modules: [nginx, sshd]
`, `
- name: windows-web
  modules: [iis]
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web.example.com"
    roles:
      - name: linux-web
        when: "os_family('linux')"
      - name: windows-web
        when: "os_family('windows')"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	if len(cfg.Modules) != 3 {
		t.Fatalf("len(Modules) = %d, want 3", len(cfg.Modules))
	}
	// nginx and sshd should inherit the role's when condition.
	if cfg.Modules[0].When != "os_family('linux')" {
		t.Errorf("nginx.When = %q, want \"os_family('linux')\"", cfg.Modules[0].When)
	}
	if cfg.Modules[1].When != "os_family('linux')" {
		t.Errorf("sshd.When = %q, want \"os_family('linux')\"", cfg.Modules[1].When)
	}
	// iis should inherit the windows role's when condition.
	if cfg.Modules[2].When != "os_family('windows')" {
		t.Errorf("iis.When = %q, want \"os_family('windows')\"", cfg.Modules[2].When)
	}
	// EnvoyConfig.Roles should list all role names.
	if len(cfg.Roles) != 2 || cfg.Roles[0] != "linux-web" || cfg.Roles[1] != "windows-web" {
		t.Errorf("Roles = %v, want [linux-web windows-web]", cfg.Roles)
	}
}

func TestConditionalRoleRefPreservesModuleWhen(t *testing.T) {
	// When both the role ref AND the module ref have when: conditions,
	// the module's own condition should take precedence.
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "modules", "certbot.vgo"), `
name: certbot
resources: []
`)
	writeRoles(t, dir, `
- name: web
  modules:
    - nginx
    - name: certbot
      when: "file_exists('/usr/bin/certbot')"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web.example.com"
    roles:
      - name: web
        when: "os_family('linux')"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	if len(cfg.Modules) != 2 {
		t.Fatalf("len(Modules) = %d, want 2", len(cfg.Modules))
	}
	// nginx has no module-level when, so it inherits the role's condition.
	if cfg.Modules[0].When != "os_family('linux')" {
		t.Errorf("nginx.When = %q, want \"os_family('linux')\"", cfg.Modules[0].When)
	}
	// certbot has its own when: condition, so role's condition should NOT override it.
	if cfg.Modules[1].When != "file_exists('/usr/bin/certbot')" {
		t.Errorf("certbot.When = %q, want \"file_exists('/usr/bin/certbot')\"", cfg.Modules[1].When)
	}
}

func TestBackwardCompatPlainStringModulesStillWork(t *testing.T) {
	// This test explicitly verifies that existing role/client YAML with plain
	// string module references continues to load without any changes.
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "certbot.vgo"), `
name: certbot
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present
`)
	writeRoles(t, dir, `
- name: webserver
  modules: [nginx, certbot]
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "bad.vgo"), `
name: bad
resources:
  - name: yolo-exec
    type: exec
    command: "echo hello"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "test.example.com"
    modules: [bad]
`)

	// Idempotency errors are non-fatal at startup — the store starts with
	// empty config and the error is stored in LastReloadError().
	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("idempotency errors should be non-fatal at startup, got: %v", err)
	}
	if cs.LastReloadError() == nil {
		t.Fatal("expected LastReloadError to be set for unguarded exec")
	}
	if !strings.Contains(cs.LastReloadError().Error(), "idempotency") {
		t.Errorf("LastReloadError should mention idempotency, got: %v", cs.LastReloadError())
	}
	// Store should have zero modules loaded.
	stats := cs.Stats()
	if stats.Modules != 0 {
		t.Errorf("expected 0 modules loaded, got %d", stats.Modules)
	}
}

func TestIdempotencyValidation_DangerousModeAllowsUnguardedExec(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "bad.vgo"), `
name: bad
resources:
  - name: yolo-exec
    type: exec
    command: "echo hello"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "good.vgo"), `
name: good
resources:
  - name: guarded-exec
    type: exec
    command: "touch /tmp/done"
    creates: "/tmp/done"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "test.example.com"
    modules: [good]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("guarded exec should pass validation, got: %v", err)
	}
}

func TestVgoFileExtension_LoadedByConfigStore(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeRoles(t, dir, `
- name: web
  modules: [nginx]
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "bad1.vgo"), `
name: bad1
resources:
  - name: yolo-exec
    type: exec
    command: "echo one"
`)
	writeFile(t, filepath.Join(dir, "modules", "bad2.vgo"), `
name: bad2
resources:
  - name: another-yolo
    type: exec
    command: "echo two"
  - name: third-yolo
    type: exec
    command: "echo three"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "test.example.com"
    modules: [bad1, bad2]
`)

	// Idempotency errors are non-fatal — store starts with empty config.
	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("idempotency errors should be non-fatal, got: %v", err)
	}

	lastErr := cs.LastReloadError()
	if lastErr == nil {
		t.Fatal("expected LastReloadError to be set")
	}

	var ie *IdempotencyErrors
	if !errors.As(lastErr, &ie) {
		t.Fatalf("expected *IdempotencyErrors, got %T: %v", lastErr, lastErr)
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
	if !strings.Contains(formatted, "validation failed") {
		t.Error("formatted output should mention validation failed")
	}

	// Store should have zero modules loaded.
	stats := cs.Stats()
	if stats.Modules != 0 {
		t.Errorf("expected 0 modules loaded, got %d", stats.Modules)
	}
}

func TestDuplicateModulesKeysMerged(t *testing.T) {
	dir := setupDir(t)

	for _, name := range []string{"docker", "grafana", "envoy-exporter"} {
		writeFile(t, filepath.Join(dir, "modules", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    package: %s
    state: present
`, name, name, name))
	}
	// Duplicate modules: keys — should merge, not overwrite.
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "danlap"
    environment: production
    modules: [docker]
    modules: [grafana]
    modules: [envoy-exporter]
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

	want := []string{"docker", "grafana", "envoy-exporter"}
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
		writeFile(t, filepath.Join(dir, "modules", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    package: %s
    state: present
`, name, name, name))
	}
	writeRoles(t, dir, `
- name: web
  modules: [nginx]
`, `
- name: cache
  modules: [redis]
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base-packages
before: [app-config]
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app-config
resources:
  - name: app-conf
    type: file
    path: /etc/app.conf
    content: "test"
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base-packages
before: [missing-module]
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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
	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base-packages
before: [app-config]
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app-config
depends_on: [base-packages]
resources:
  - name: app-conf
    type: file
    path: /etc/app.conf
    content: "test"
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
vars:
  port: "8080"
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "web.vgo"), `
server_name: "web01.example.com"
port: "9090"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01.example.com"
    vars_from: "vars/web.vgo"
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
vars:
  port: "8080"
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "common.vgo"), `
port: "3000"
log_level: "info"
`)
	writeFile(t, filepath.Join(dir, "vars", "host.vgo"), `
port: "4000"
server_name: "host1"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "host1.example.com"
    vars_from:
      - "vars/common.vgo"
      - "vars/host.vgo"
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "defaults.vgo"), `
port: "3000"
log_level: "info"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "host1.example.com"
    vars_from: "vars/defaults.vgo"
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
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
	writeFile(t, filepath.Join(dir, "vars", "data.vgo"), `
a: "file"
b: "file"
d: "file"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "host1.example.com"
    vars_from: "vars/data.vgo"
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "host1.example.com"
    vars_from: "vars/nonexistent.vgo"
    modules: [app]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for missing vars_from file")
	}
	if !strings.Contains(err.Error(), "nonexistent.vgo") {
		t.Errorf("error should mention the missing file, got: %v", err)
	}
}

func TestVarsFromPathTraversal(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
vars:
  port: "8080"
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "empty.vgo"), ``)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "host1.example.com"
    vars_from: "vars/empty.vgo"
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "secrets.vgo"), `
api_key: "secret:app/api_key"
debug: "false"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "host1.example.com"
    vars_from: "vars/secrets.vgo"
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "data.vgo"), `
color: "blue"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "host1.example.com"
    vars_from: "vars/data.vgo"
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
vars:
  z_module: "from_module"
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "vars", "data.vgo"), `
b_file: "from_file"
a_file: "from_file"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "host1.example.com"
    vars_from: "vars/data.vgo"
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "modules", "apache.vgo"), `
name: apache
resources:
  - name: install
    type: package
    package: apache2
    state: present
`)
	// Two glob patterns that both match "web01.prod.example.com".
	// First match should win.
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

func TestEmptyEnvoysDir(t *testing.T) {
	dir := setupDir(t)

	// No envoy files, no modules, no roles — just empty dirs.
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
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
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "webserver.vgo"), `
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
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: install
    type: package
    package: app
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: conf
    type: file
    target_path: /etc/app/config
    source: "../../etc/shadow"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: conf
    type: file
    target_path: /etc/app/config
    source: "/etc/passwd"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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
				if cfg.Envoy != "web01.example.com" {
					t.Errorf("Get() returned wrong envoy: %q", cfg.Envoy)
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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
	outsideFile := filepath.Join(t.TempDir(), "outside.vgo")
	writeFile(t, outsideFile, "stolen_var: gotcha\n")

	symlinkPath := filepath.Join(varsDir, "evil.vgo")
	if err := os.Symlink(outsideFile, symlinkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	// loadVarsFile should reject the symlink.
	_, _, err := loadVarsFile(dir, "vars/evil.vgo")
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
	writeFile(t, filepath.Join(dir, "modules", "evil.vgo"), `
name: evil
resources:
  - name: evil-file
    type: file
    target_path: /etc/evil.conf
    source: templates/evil.conf
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "firewall.vgo"), `
name: firewall
resources:
  - name: fw-rules
    type: file
    target_path: /etc/iptables/rules.v4
`)
	writeFile(t, filepath.Join(dir, "modules", "sshd.vgo"), `
name: sshd
resources:
  - name: sshd-config
    type: file
    target_path: /etc/ssh/sshd_config
`)
	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeRoles(t, dir, `
- name: base-security
  modules:
    - firewall
    - sshd
`, `
- name: webserver
  includes:
    - base-security
  modules:
    - nginx
`)
	writeFile(t, filepath.Join(dir, "default", "web.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "a.vgo"), `
name: a
resources:
  - name: r
    type: file
    target_path: /tmp/a
`)
	writeRoles(t, dir, `
- name: inner
  includes:
    - outer
  modules:
    - a
`, `
- name: outer
  includes:
    - inner
  modules:
    - a
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "shared.vgo"), `
name: shared
resources:
  - name: r
    type: file
    target_path: /tmp/shared
`)
	writeFile(t, filepath.Join(dir, "modules", "web.vgo"), `
name: web
resources:
  - name: r
    type: file
    target_path: /tmp/web
`)
	writeRoles(t, dir, `
- name: base
  modules:
    - shared
`, `
- name: webserver
  includes:
    - base
  modules:
    - shared
    - web
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
vars:
  port: "8080"
resources:
  - name: cfg
    type: file
    target_path: /etc/app.conf
`)

	varsDir := filepath.Join(dir, "vars")
	if err := os.MkdirAll(varsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(varsDir, "web01.example.com.vgo"), `
port: "9090"
`)

	writeFile(t, filepath.Join(dir, "default", "web.vgo"), `
envoys:
  - match: "web01.example.com"
    modules: [app]
    vars_from:
      - "vars/{{ .Hostname }}.vgo"
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: cfg
    type: file
    target_path: /etc/app.conf
`)

	varsDir := filepath.Join(dir, "vars")
	if err := os.MkdirAll(varsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(varsDir, "host1.vgo"), `
key: val
`)

	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "host1"
    modules: [app]
    vars_from:
      - "vars/{{.Hostname}}.vgo"
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

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
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
    target_path: /etc/nginx/nginx.conf
`)
	writeFile(t, filepath.Join(dir, "modules", "firewall.vgo"), `
name: firewall
resources:
  - name: fw
    type: file
    target_path: /etc/iptables/rules.v4
`)
	writeRoles(t, dir, `
- name: base
  modules:
    - firewall
`, `
- name: web
  includes:
    - base
  modules:
    - nginx
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
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
	writeFile(t, filepath.Join(dir, "modules", "a.vgo"), `
name: a
resources:
  - name: r
    type: file
    target_path: /tmp/a
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
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

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: r
    type: file
    target_path: /tmp/app
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
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

// --- Directory Inheritance Tests ---

// setupInheritanceDir creates a config directory using the new flat layout
// (no envoys/ subdirectory, common.vgo for inheritance).
func setupInheritanceDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(filepath.Join(dir, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestInheritance_BasicModules(t *testing.T) {
	dir := setupInheritanceDir(t)

	writeFile(t, filepath.Join(dir, "modules", "sshd.vgo"), `
name: sshd
resources:
  - name: install-sshd
    type: package
    package: openssh-server
`)
	writeFile(t, filepath.Join(dir, "modules", "monitoring.vgo"), `
name: monitoring
resources:
  - name: install-monitoring
    type: package
    package: prometheus-node-exporter
`)
	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
`)

	// Root common.vgo — inherited by all subdirs.
	writeFile(t, filepath.Join(dir, "common.vgo"), `
modules: [sshd, monitoring]
`)

	// Leaf entry in a subdirectory.
	os.MkdirAll(filepath.Join(dir, "web"), 0o755)
	writeFile(t, filepath.Join(dir, "web", "web.vgo"), `
envoys:
  - match: "web*.example.com"
    modules: [nginx]
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := cs.Get("web01.example.com")
	if cfg == nil {
		t.Fatal("expected config for web01.example.com")
	}

	// Should have inherited sshd + monitoring, plus own nginx.
	var moduleNames []string
	for _, m := range cfg.Modules {
		moduleNames = append(moduleNames, m.Name)
	}

	if len(moduleNames) != 3 {
		t.Fatalf("expected 3 modules, got %d: %v", len(moduleNames), moduleNames)
	}
	// Parent modules first, then own.
	if moduleNames[0] != "sshd" {
		t.Errorf("module[0] = %q, want sshd", moduleNames[0])
	}
	if moduleNames[1] != "monitoring" {
		t.Errorf("module[1] = %q, want monitoring", moduleNames[1])
	}
	if moduleNames[2] != "nginx" {
		t.Errorf("module[2] = %q, want nginx", moduleNames[2])
	}
}

func TestInheritance_MultiLevel(t *testing.T) {
	dir := setupInheritanceDir(t)

	for _, name := range []string{"sshd", "logging", "nginx"} {
		writeFile(t, filepath.Join(dir, "modules", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    package: %s
`, name, name, name))
	}

	writeFile(t, filepath.Join(dir, "common.vgo"), `
modules: [sshd]
`)

	os.MkdirAll(filepath.Join(dir, "production"), 0o755)
	writeFile(t, filepath.Join(dir, "production", "common.vgo"), `
modules: [logging]
`)

	os.MkdirAll(filepath.Join(dir, "production", "web"), 0o755)
	writeFile(t, filepath.Join(dir, "production", "web", "web.vgo"), `
envoys:
  - match: "web*.prod.example.com"
    modules: [nginx]
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := cs.Get("web01.prod.example.com")
	if cfg == nil {
		t.Fatal("expected config")
	}

	var names []string
	for _, m := range cfg.Modules {
		names = append(names, m.Name)
	}
	// root(sshd) → production(logging) → web(nginx)
	want := []string{"sshd", "logging", "nginx"}
	if len(names) != len(want) {
		t.Fatalf("modules = %v, want %v", names, want)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("module[%d] = %q, want %q", i, names[i], w)
		}
	}
}

func TestInheritance_ExcludeModules(t *testing.T) {
	dir := setupInheritanceDir(t)

	for _, name := range []string{"sshd", "monitoring", "nginx"} {
		writeFile(t, filepath.Join(dir, "modules", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    package: %s
`, name, name, name))
	}

	writeFile(t, filepath.Join(dir, "common.vgo"), `
modules: [sshd, monitoring]
`)

	os.MkdirAll(filepath.Join(dir, "containers"), 0o755)
	writeFile(t, filepath.Join(dir, "containers", "containers.vgo"), `
envoys:
  - match: "docker*.example.com"
    modules: [nginx]
    exclude_modules: [monitoring]
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := cs.Get("docker01.example.com")
	if cfg == nil {
		t.Fatal("expected config")
	}

	var names []string
	for _, m := range cfg.Modules {
		names = append(names, m.Name)
	}
	// sshd inherited, monitoring excluded, nginx own.
	want := []string{"sshd", "nginx"}
	if len(names) != len(want) {
		t.Fatalf("modules = %v, want %v", names, want)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("module[%d] = %q, want %q", i, names[i], w)
		}
	}
}

func TestInheritance_VarsOverride(t *testing.T) {
	dir := setupInheritanceDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: install-app
    type: package
    package: app
`)

	writeFile(t, filepath.Join(dir, "common.vgo"), `
modules: [app]
vars:
  log_level: info
  dns_server: "1.1.1.1"
`)

	os.MkdirAll(filepath.Join(dir, "staging"), 0o755)
	writeFile(t, filepath.Join(dir, "staging", "staging.vgo"), `
envoys:
  - match: "*.staging.example.com"
    vars:
      log_level: debug
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := cs.Get("app01.staging.example.com")
	if cfg == nil {
		t.Fatal("expected config")
	}

	// log_level should be overridden to debug, dns_server inherited.
	if cfg.Vars["log_level"] != "debug" {
		t.Errorf("log_level = %v, want debug", cfg.Vars["log_level"])
	}
	if cfg.Vars["dns_server"] != "1.1.1.1" {
		t.Errorf("dns_server = %v, want 1.1.1.1", cfg.Vars["dns_server"])
	}
}

func TestInheritance_SubdirInheritsFromRoot(t *testing.T) {
	// Entries in subdirectories inherit from root common.vgo.
	// Entries in any subdirectory (including one named "envoys") inherit.
	dir := setupInheritanceDir(t)

	writeFile(t, filepath.Join(dir, "modules", "sshd.vgo"), `
name: sshd
resources:
  - name: install-sshd
    type: package
    package: openssh-server
`)
	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: install-app
    type: package
    package: myapp
`)

	writeFile(t, filepath.Join(dir, "common.vgo"), `
modules: [sshd]
`)

	os.MkdirAll(filepath.Join(dir, "servers"), 0o755)
	writeFile(t, filepath.Join(dir, "servers", "all.vgo"), `
envoys:
  - match: "srv*.example.com"
    modules: [app]
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := cs.Get("srv01.example.com")
	if cfg == nil {
		t.Fatal("expected config")
	}

	var names []string
	for _, m := range cfg.Modules {
		names = append(names, m.Name)
	}
	// Should have inherited sshd + own app.
	want := []string{"sshd", "app"}
	if len(names) != len(want) {
		t.Fatalf("modules = %v, want %v", names, want)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("module[%d] = %q, want %q", i, names[i], w)
		}
	}
}

func TestInheritance_TraceShowsChain(t *testing.T) {
	dir := setupInheritanceDir(t)

	for _, name := range []string{"sshd", "nginx"} {
		writeFile(t, filepath.Join(dir, "modules", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    package: %s
`, name, name, name))
	}

	writeFile(t, filepath.Join(dir, "common.vgo"), `
modules: [sshd]
`)

	os.MkdirAll(filepath.Join(dir, "web"), 0o755)
	writeFile(t, filepath.Join(dir, "web", "web.vgo"), `
envoys:
  - match: "web*.example.com"
    modules: [nginx]
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := cs.Trace("web01.example.com")
	if !trace.Matched {
		t.Fatal("expected match")
	}

	if len(trace.Inheritance) != 2 {
		t.Fatalf("expected 2 inheritance levels, got %d", len(trace.Inheritance))
	}
	if trace.Inheritance[0].File != "common.vgo" {
		t.Errorf("inheritance[0].File = %q, want common.vgo", trace.Inheritance[0].File)
	}
	if len(trace.Inheritance[0].Modules) != 1 || trace.Inheritance[0].Modules[0] != "sshd" {
		t.Errorf("inheritance[0].Modules = %v, want [sshd]", trace.Inheritance[0].Modules)
	}
	// Leaf entry level shows the source file.
	if trace.Inheritance[1].File != "web.vgo" {
		t.Errorf("inheritance[1].File = %q, want web.vgo", trace.Inheritance[1].File)
	}
	if trace.Inheritance[1].Dir != "web" {
		t.Errorf("inheritance[1].Dir = %q, want web", trace.Inheritance[1].Dir)
	}
	if len(trace.Inheritance[1].Modules) != 1 || trace.Inheritance[1].Modules[0] != "nginx" {
		t.Errorf("inheritance[1].Modules = %v, want [nginx]", trace.Inheritance[1].Modules)
	}
}

func TestInheritance_InheritedRoles(t *testing.T) {
	dir := setupInheritanceDir(t)

	for _, name := range []string{"sshd", "monitoring", "nginx"} {
		writeFile(t, filepath.Join(dir, "modules", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    package: %s
`, name, name, name))
	}

	// roles.vgo at root.
	writeFile(t, filepath.Join(dir, "roles.vgo"), `
roles:
  - name: base-security
    modules: [sshd]
`)

	writeFile(t, filepath.Join(dir, "common.vgo"), `
roles: [base-security]
modules: [monitoring]
`)

	os.MkdirAll(filepath.Join(dir, "web"), 0o755)
	writeFile(t, filepath.Join(dir, "web", "web.vgo"), `
envoys:
  - match: "web*.example.com"
    modules: [nginx]
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := cs.Get("web01.example.com")
	if cfg == nil {
		t.Fatal("expected config")
	}

	var names []string
	for _, m := range cfg.Modules {
		names = append(names, m.Name)
	}
	// Inherited role base-security(sshd) + inherited module monitoring + own nginx.
	want := []string{"sshd", "monitoring", "nginx"}
	if len(names) != len(want) {
		t.Fatalf("modules = %v, want %v", names, want)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("module[%d] = %q, want %q", i, names[i], w)
		}
	}
}

func TestInheritance_CommonVarsFromFile(t *testing.T) {
	dir := setupInheritanceDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
resources:
  - name: install-app
    type: package
    package: app
`)

	// common.vgo with vars defined at directory level.
	os.MkdirAll(filepath.Join(dir, "production"), 0o755)
	writeFile(t, filepath.Join(dir, "common.vgo"), `
modules: [app]
vars:
  region: us-west
`)
	writeFile(t, filepath.Join(dir, "production", "common.vgo"), `
vars:
  environment: production
`)

	writeFile(t, filepath.Join(dir, "production", "prod.vgo"), `
envoys:
  - match: "*.prod.example.com"
    vars:
      instance_type: large
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := cs.Get("app01.prod.example.com")
	if cfg == nil {
		t.Fatal("expected config")
	}

	// region from root common, environment from production common, instance_type from entry.
	if cfg.Vars["region"] != "us-west" {
		t.Errorf("region = %v, want us-west", cfg.Vars["region"])
	}
	if cfg.Vars["environment"] != "production" {
		t.Errorf("environment = %v, want production", cfg.Vars["environment"])
	}
	if cfg.Vars["instance_type"] != "large" {
		t.Errorf("instance_type = %v, want large", cfg.Vars["instance_type"])
	}
}

// --- New function tests: compliance index, when display, var priority ---

func TestBuildComplianceIndex(t *testing.T) {
	mods := map[string]*ModuleDefinition{
		"sshd": {
			Name: "sshd",
			Compliance: map[string][]string{
				"hipaa":   {"AC-1", "SC-8"},
				"pci-dss": {"2.2.1", "8.3.1"},
			},
		},
		"nginx": {
			Name: "nginx",
			Compliance: map[string][]string{
				"pci-dss": {"6.5.1"},
			},
		},
		"motd": {Name: "motd"}, // no compliance tags
	}

	idx := buildComplianceIndex(mods)

	// hipaa should have 2 controls from sshd.
	if len(idx["hipaa"]) != 2 {
		t.Fatalf("hipaa controls = %d, want 2", len(idx["hipaa"]))
	}
	if len(idx["hipaa"]["AC-1"]) != 1 || idx["hipaa"]["AC-1"][0] != "sshd" {
		t.Errorf("hipaa AC-1 modules = %v, want [sshd]", idx["hipaa"]["AC-1"])
	}

	// pci-dss should have 3 controls: 2 from sshd + 1 from nginx.
	if len(idx["pci-dss"]) != 3 {
		t.Fatalf("pci-dss controls = %d, want 3", len(idx["pci-dss"]))
	}
	if len(idx["pci-dss"]["6.5.1"]) != 1 || idx["pci-dss"]["6.5.1"][0] != "nginx" {
		t.Errorf("pci-dss 6.5.1 modules = %v, want [nginx]", idx["pci-dss"]["6.5.1"])
	}

	// motd has no compliance tags — should not appear.
	if _, ok := idx["motd"]; ok {
		t.Error("motd should not be in compliance index")
	}
}

func TestEnvoyStandardCoverage(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "sshd.vgo"), `
name: sshd
compliance:
  hipaa: [AC-1, SC-8]
  pci-dss: ["2.2.1", "8.3.1"]
resources:
  - name: sshd-config
    type: file
    target_path: /etc/ssh/sshd_config
`)
	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
compliance:
  pci-dss: ["6.5.1"]
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    modules: [sshd, nginx]
  - match: "db01"
    modules: [sshd]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// web01 has both sshd and nginx — should cover all 3 pci-dss controls.
	cov := store.EnvoyStandardCoverage("web01", "pci-dss")
	if cov == nil {
		t.Fatal("expected coverage, got nil")
	}
	if cov.TotalControls != 3 {
		t.Errorf("TotalControls = %d, want 3", cov.TotalControls)
	}
	if len(cov.CoveredControls) != 3 {
		t.Errorf("CoveredControls = %d, want 3", len(cov.CoveredControls))
	}
	if len(cov.MissingControls) != 0 {
		t.Errorf("MissingControls = %v, want empty", cov.MissingControls)
	}
	if cov.CoveragePercent != 100 {
		t.Errorf("CoveragePercent = %.1f, want 100", cov.CoveragePercent)
	}

	// db01 has only sshd — should cover 2 of 3 pci-dss controls, missing 6.5.1.
	cov2 := store.EnvoyStandardCoverage("db01", "pci-dss")
	if cov2 == nil {
		t.Fatal("expected coverage for db01, got nil")
	}
	if len(cov2.CoveredControls) != 2 {
		t.Errorf("db01 CoveredControls = %d, want 2", len(cov2.CoveredControls))
	}
	if len(cov2.MissingControls) != 1 || cov2.MissingControls[0] != "6.5.1" {
		t.Errorf("db01 MissingControls = %v, want [6.5.1]", cov2.MissingControls)
	}

	// Unknown standard returns nil.
	if store.EnvoyStandardCoverage("web01", "nonexistent") != nil {
		t.Error("expected nil for unknown standard")
	}

	// Unknown hostname returns empty coverage (no modules assigned).
	cov3 := store.EnvoyStandardCoverage("unknown", "pci-dss")
	if cov3 == nil {
		t.Fatal("expected coverage struct for unknown host, got nil")
	}
	if len(cov3.CoveredControls) != 0 {
		t.Errorf("unknown host should have 0 covered, got %d", len(cov3.CoveredControls))
	}
}

func TestVarSourcePriority(t *testing.T) {
	tests := []struct {
		source   string
		priority int
	}{
		{"module:sshd", 0},
		{"module:nginx", 0},
		{"vars_from:vars/web.vgo", 1},
		{"inline", 2},
		{"environment_overrides:production", 3},
		{"unknown", 4},
		{"", 4},
	}
	for _, tt := range tests {
		got := varSourcePriority(tt.source)
		if got != tt.priority {
			t.Errorf("varSourcePriority(%q) = %d, want %d", tt.source, got, tt.priority)
		}
	}
}

func TestClassifyWhen(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{"is_debian", "server"},
		{"not is_container", "server"},
		{"is_debian and is_webserver", "server"},
		{"os_family('debian')", "agent"},
		{"not os_family('debian')", "agent"},
		{"is_debian and os_family('redhat')", "agent"},
		{"true", "server"},
		{"", "server"},
	}
	for _, tt := range tests {
		got := classifyWhen(tt.expr)
		if got != tt.want {
			t.Errorf("classifyWhen(%q) = %q, want %q", tt.expr, got, tt.want)
		}
	}
}

func TestRenderWhenTemplates(t *testing.T) {
	vars := map[string]any{
		"scan_hour_start": "06",
		"scan_hour_end":   "22",
	}

	tests := []struct {
		expr string
		want string
	}{
		// Template expressions should be rendered.
		{"hour_range('{{ .Vars.scan_hour_start }}','{{ .Vars.scan_hour_end }}')", "hour_range('06','22')"},
		// No templates — returned as-is.
		{"os_family('debian')", "os_family('debian')"},
		{"is_webserver", "is_webserver"},
		// Missing var — Go template renders as "<no value>".
		{"hour_range('{{ .Vars.missing }}')", "hour_range('<no value>')"},
	}
	for _, tt := range tests {
		got := renderWhenTemplates(tt.expr, vars)
		if got != tt.want {
			t.Errorf("renderWhenTemplates(%q) = %q, want %q", tt.expr, got, tt.want)
		}
	}
}

func TestResolveWhenDisplay(t *testing.T) {
	vars := map[string]any{
		"is_webserver": true,
		"is_container": false,
	}

	tests := []struct {
		expr string
		want string
	}{
		// Var resolved to value.
		{"is_webserver", "true"},
		// Negated var.
		{"not is_container", "!false"},
		// Function call stays plain (not wrapped — no var substitution).
		{"os_family('debian')", "os_family('debian')"},
		// Mixed: var resolved, function stays plain.
		{"is_webserver and os_family('debian')", "true && os_family('debian')"},
		// Unknown var stays as name.
		{"unknown_var", "unknown_var"},
	}
	for _, tt := range tests {
		got := resolveWhenDisplay(tt.expr, vars)
		if got != tt.want {
			t.Errorf("resolveWhenDisplay(%q) = %q, want %q", tt.expr, got, tt.want)
		}
	}
}

func TestTraceSecretRefs(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "db.vgo"), `
name: db
vars:
  dsn: "secret:vigo/db/dsn"
resources:
  - name: db-config
    type: file
    target_path: /etc/db.conf
    content: "dsn={{ .Vars.dsn }}"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "db01"
    modules: [db]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{
		"vigo/db/dsn": "postgres://localhost/vigo",
	}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("db01")
	if !trace.Matched {
		t.Fatal("expected matched")
	}
	if len(trace.SecretRefs) == 0 {
		t.Fatal("expected SecretRefs, got none")
	}

	// Should find the secret var reference.
	found := false
	for _, sr := range trace.SecretRefs {
		if sr.Key == "dsn" && sr.Path == "vigo/db/dsn" && sr.Module == "db" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SecretRef for dsn, got %v", trace.SecretRefs)
	}
}

func TestTraceDependencies(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
depends_on: [sshd]
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
  - name: nginx-config
    type: file
    target_path: /etc/nginx/nginx.conf
    notify: [nginx-service]
  - name: nginx-service
    type: service
    service: nginx
    state: running
    depends_on: [install-nginx]
`)
	writeFile(t, filepath.Join(dir, "modules", "sshd.vgo"), `
name: sshd
resources:
  - name: sshd-config
    type: file
    target_path: /etc/ssh/sshd_config
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    modules: [sshd, nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("web01")
	if len(trace.Dependencies) == 0 {
		t.Fatal("expected dependencies, got none")
	}

	// Check module-level depends_on: nginx → sshd.
	foundModDep := false
	foundResNotify := false
	foundResDep := false
	for _, d := range trace.Dependencies {
		if d.From == "nginx" && d.To == "sshd" && d.Type == "depends_on" && d.Level == "module" {
			foundModDep = true
		}
		if d.From == "nginx/nginx-config" && d.To == "nginx/nginx-service" && d.Type == "notify" {
			foundResNotify = true
		}
		if d.From == "nginx/nginx-service" && d.To == "nginx/install-nginx" && d.Type == "depends_on" && d.Level == "resource" {
			foundResDep = true
		}
	}
	if !foundModDep {
		t.Error("missing module depends_on: nginx → sshd")
	}
	if !foundResNotify {
		t.Error("missing resource notify: nginx-config → nginx-service")
	}
	if !foundResDep {
		t.Error("missing resource depends_on: nginx-service → install-nginx")
	}
}

func TestTraceComplianceTags(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "sshd.vgo"), `
name: sshd
compliance:
  hipaa: [AC-1, SC-8]
  pci-dss: ["2.2.1"]
resources:
  - name: sshd-config
    type: file
    target_path: /etc/ssh/sshd_config
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    modules: [sshd]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("web01")
	if len(trace.ComplianceTags) != 1 {
		t.Fatalf("ComplianceTags = %d, want 1", len(trace.ComplianceTags))
	}
	ct := trace.ComplianceTags[0]
	if ct.Module != "sshd" {
		t.Errorf("Module = %q, want sshd", ct.Module)
	}
	if len(ct.Frameworks["hipaa"]) != 2 {
		t.Errorf("hipaa controls = %d, want 2", len(ct.Frameworks["hipaa"]))
	}
	if len(ct.Frameworks["pci-dss"]) != 1 {
		t.Errorf("pci-dss controls = %d, want 1", len(ct.Frameworks["pci-dss"]))
	}
}

func TestTraceConditionals(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "scanner.vgo"), `
name: scanner
resources:
  - name: run-scan
    type: exec
    command: /usr/bin/scan
    when: "os_family('debian')"
  - name: check-status
    type: exec
    command: /usr/bin/status
    when: "is_enabled"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    modules: [scanner]
    vars:
      is_enabled: true
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("web01")
	if len(trace.Conditionals) != 2 {
		t.Fatalf("Conditionals = %d, want 2", len(trace.Conditionals))
	}

	// os_family('debian') should be agent-side.
	c0 := trace.Conditionals[0]
	if c0.EvalSide != "agent" {
		t.Errorf("run-scan EvalSide = %q, want agent", c0.EvalSide)
	}
	if c0.Resource != "run-scan" {
		t.Errorf("Resource = %q, want run-scan", c0.Resource)
	}

	// is_enabled should be server-side with resolved value.
	c1 := trace.Conditionals[1]
	if c1.EvalSide != "server" {
		t.Errorf("check-status EvalSide = %q, want server", c1.EvalSide)
	}
	if !strings.Contains(c1.ResolvedWhen, "true") {
		t.Errorf("ResolvedWhen = %q, want to contain 'true'", c1.ResolvedWhen)
	}
}

func TestTraceVarSourceOrdering(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.vgo"), `
name: app
vars:
  port: "8080"
  log_level: "info"
resources:
  - name: app-config
    type: file
    target_path: /etc/app.conf
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    modules: [app]
    environment: production
    vars:
      port: "9090"
    environment_overrides:
      production:
        log_level: "warn"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("web01")

	// VarSources should be ordered: module (0) before inline (2) before env_overrides (3).
	if len(trace.VarSources) < 2 {
		t.Fatalf("VarSources = %d, want >= 2", len(trace.VarSources))
	}

	priorities := make([]int, len(trace.VarSources))
	for i, vs := range trace.VarSources {
		priorities[i] = varSourcePriority(vs.Source)
	}
	for i := 1; i < len(priorities); i++ {
		if priorities[i] < priorities[i-1] {
			t.Errorf("VarSources not ordered trunk-to-leaf: priority[%d]=%d < priority[%d]=%d (keys: %s, %s)",
				i, priorities[i], i-1, priorities[i-1],
				trace.VarSources[i].Key, trace.VarSources[i-1].Key)
		}
	}
}

func TestFilterByModuleFiltersNewSections(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "sshd.vgo"), `
name: sshd
compliance:
  hipaa: [AC-1]
resources:
  - name: sshd-config
    type: file
    target_path: /etc/ssh/sshd_config
    when: "is_enabled"
`)
	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
compliance:
  pci-dss: ["6.5.1"]
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
    when: "os_family('debian')"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    modules: [sshd, nginx]
    vars:
      is_enabled: true
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("web01")
	filtered := trace.FilterByModule("nginx")

	// Compliance should only include nginx.
	if len(filtered.ComplianceTags) != 1 || filtered.ComplianceTags[0].Module != "nginx" {
		t.Errorf("filtered ComplianceTags = %v, want [nginx]", filtered.ComplianceTags)
	}

	// Conditionals should only include nginx resources.
	for _, c := range filtered.Conditionals {
		if c.Module != "nginx" {
			t.Errorf("filtered Conditional module = %q, want nginx", c.Module)
		}
	}
}
