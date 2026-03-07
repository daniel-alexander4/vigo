package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// setupDir creates a client-configs directory with modules/ and roles/ subdirs.
func setupDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "client-configs")
	for _, sub := range []string{"modules", "roles"} {
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

	writeFile(t, filepath.Join(dir, "webservers.yaml"), `
clients:
  - match: "test.example.com"
    environment: production
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("test.example.com")
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "web01.example.com"
    environment: production
    roles: [webserver]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "web01.example.com"
    environment: production
    modules: [nginx]
    vars:
      worker_processes: "4"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "web.example.com"
    environment: production
    modules: [app]
    vars:
      db_dsn: "secret:isogov/db/dsn"
`)

	mp := &mockProvider{secrets: map[string]string{
		"isogov/db/dsn":   "postgres://real:pw@db:5432/prod",
		"myapp/db/config": "host=db port=5432 user=myapp",
	}}
	store, err := NewConfigStore(dir, mp)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	if cfg.Vars["db_dsn"] != "postgres://real:pw@db:5432/prod" {
		t.Errorf("db_dsn = %q, want resolved DSN", cfg.Vars["db_dsn"])
	}
}

func TestKnownSecretEnforcement(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "app.yaml"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "bad.example.com"
    environment: production
    modules: [app]
    vars:
      dsn: "postgres://plaintext@db/bad"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "fail.example.com"
    environment: production
    modules: [app]
    vars:
      db_conn: "secret:nonexistent/key"
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
	if err == nil {
		t.Fatal("expected error for unresolvable secret, got nil")
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "*.prod.example.com"
    environment: production
    modules: [base, nginx]
  - match: "*.staging.example.com"
    environment: staging
    modules: [base]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "web01.prod.example.com"
    environment: staging
    modules: [explicit]
  - match: "*.prod.example.com"
    environment: production
    modules: [pattern]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "webservers.yaml"), `
clients:
  - match: "web-*"
    environment: production
    modules: [nginx]
`)
	writeFile(t, filepath.Join(dir, "databases.yaml"), `
clients:
  - match: "db-*"
    environment: production
    modules: [postgres]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "envtest.example.com"
    environment: production
    modules: [app]
    vars:
      app_port: "8080"
      log_level: "info"
    env_vars:
      production:
        app_port: "443"
        max_workers: "8"
      staging:
        app_port: "8443"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "new-hosts.yaml"), `
clients:
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "good.example.com"
    environment: production
    modules: [app]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
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

	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "bad.example.com"
    modules: [nonexistent]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
	if err == nil {
		t.Fatal("expected error for unknown module, got nil")
	}
}

func TestUnknownRoleError(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "bad.example.com"
    roles: [nonexistent]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "dup.example.com"
    roles: [web]
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("dup.example.com")
	if len(cfg.Modules) != 1 {
		t.Errorf("expected 1 module (deduped), got %d", len(cfg.Modules))
	}
}

func TestClientsReferencingSecret(t *testing.T) {
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "web-*"
    modules: [app]
  - match: "db-*"
    modules: [other]
`)

	mp := &mockProvider{secrets: map[string]string{
		"myapp/db/config": "resolved-value",
	}}
	store, err := NewConfigStore(dir, mp)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	patterns := store.ClientsReferencingSecret("myapp/db/config")
	if len(patterns) != 1 || patterns[0] != "web-*" {
		t.Errorf("expected [web-*], got %v", patterns)
	}

	patterns = store.ClientsReferencingSecret("nonexistent")
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "web.example.com"
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "test.example.com"
    modules: [bad]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "test.example.com"
    modules: [bad]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "web.example.com"
    modules: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
		facts    map[string]any
		varOrder []string
		want     map[string]any
		wantErr  bool
	}{
		{
			name: "facts reference renders correctly",
			vars: map[string]any{
				"greeting": "Hello from {{ .Traits.os.distro }}",
			},
			facts: map[string]any{
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
			facts:    map[string]any{},
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
			facts: map[string]any{
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
			facts:    map[string]any{},
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
			facts:    map[string]any{},
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
			facts:    map[string]any{},
			varOrder: []string{"bad"},
			wantErr:  true,
		},
		{
			name:     "empty vars map returns empty map",
			vars:     map[string]any{},
			facts:    map[string]any{},
			varOrder: nil,
			want:     map[string]any{},
		},
		{
			name: "nil varOrder falls back to map iteration",
			vars: map[string]any{
				"greeting": "Hello from {{ .Traits.os.distro }}",
			},
			facts: map[string]any{
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
			got, err := RenderVars(tt.vars, tt.facts, tt.varOrder)
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
    only: [debian, ubuntu]
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)

	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "web.example.com"
    modules: [webserver]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
	if cv.Operator != "only" {
		t.Errorf("ConditionalVar operator = %q, want 'only'", cv.Operator)
	}
}

func TestLoadModuleInvalidConditionalVar(t *testing.T) {
	dir := setupDir(t)

	// Module with a bad conditional var (no trait field).
	writeFile(t, filepath.Join(dir, "modules", "bad.yaml"), `
name: bad
vars:
  broken_cond:
    only: [debian, ubuntu]
resources: []
`)

	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "test.example.com"
    modules: [bad]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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

	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "test.example.com"
    modules: [bad]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
    only: [ubuntu]
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
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "multi.example.com"
    modules: [mod_a, mod_b]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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
    only: [debian, ubuntu]
resources: []
`)
	writeFile(t, filepath.Join(dir, "clients.yaml"), `
clients:
  - match: "override.example.com"
    modules: [mod]
    vars:
      is_debian: "manual_override"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}})
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

func TestTemplateRendering(t *testing.T) {
	facts := map[string]any{
		"os": map[string]any{"distro": "ubuntu"},
	}
	vars := map[string]any{
		"app_port": "8080",
	}

	result, err := RenderTemplate("port={{ .Vars.app_port }} os={{ .Traits.os.distro }}", facts, vars)
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if result != "port=8080 os=ubuntu" {
		t.Errorf("got %q, want 'port=8080 os=ubuntu'", result)
	}
}
