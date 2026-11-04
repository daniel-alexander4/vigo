// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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

// setupDir creates a content-root config directory: modules/ for the
// module library, default/ for envoy entries. The returned path is what
// the loader walks directly (post-flattening — no inner stacks/).
func setupDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "examples", "stacks")
	for _, sub := range []string{"configcrates", "usercrates", "templates", "default"} {
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

func TestLookupsInResourceAttributes(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "users.vgo"), `
name: users
resources:
  - name: dan
    type: user
    state: present
    username: dan
    groups:
      tag:workstation: [docker, virtualbox]
      tag:mediaserver: [docker, plex]
      default:         [docker]
    shell:
      danlap:  /bin/fish
      default: /bin/bash
`)

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "danlap"
    configcrates: [users]
    tags: [workstation]
  - match: "plex.home"
    configcrates: [users]
    tags: [mediaserver]
  - match: "nginx.home"
    configcrates: [users]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// Detection: all three configs should have HasLookups=true because
	// the shared module has lookup attributes.
	for _, h := range []string{"danlap", "plex.home", "nginx.home"} {
		cfg := store.Get(h)
		if !cfg.HasLookups {
			t.Errorf("%s: HasLookups = false, want true", h)
		}
	}

	// Resolution: the store returns the shared cfg with unresolved lookups.
	// The check-in path clones and calls ResolveLookupsInConfig. Simulate here.
	resolve := func(hostname string) *EnvoyConfig {
		cfg := store.Get(hostname).Clone()
		if err := ResolveLookupsInConfig(cfg, hostname, nil); err != nil {
			t.Fatalf("%s: resolve: %v", hostname, err)
		}
		return cfg
	}

	danlap := resolve("danlap")
	danRes := danlap.Configcrates[0].Resources[0]
	if got := danRes.Attributes["shell"]; got != "/bin/fish" {
		t.Errorf("danlap shell = %v, want /bin/fish", got)
	}
	gotGroups, _ := danRes.Attributes["groups"].([]any)
	if len(gotGroups) != 2 || gotGroups[0] != "docker" || gotGroups[1] != "virtualbox" {
		t.Errorf("danlap groups = %v, want [docker virtualbox]", gotGroups)
	}

	plex := resolve("plex.home")
	plexRes := plex.Configcrates[0].Resources[0]
	if got := plexRes.Attributes["shell"]; got != "/bin/bash" {
		t.Errorf("plex.home shell = %v, want /bin/bash", got)
	}
	gotGroups, _ = plexRes.Attributes["groups"].([]any)
	if len(gotGroups) != 2 || gotGroups[0] != "docker" || gotGroups[1] != "plex" {
		t.Errorf("plex.home groups = %v, want [docker plex]", gotGroups)
	}

	nginx := resolve("nginx.home")
	nginxRes := nginx.Configcrates[0].Resources[0]
	if got := nginxRes.Attributes["shell"]; got != "/bin/bash" {
		t.Errorf("nginx.home shell = %v, want /bin/bash", got)
	}
	gotGroups, _ = nginxRes.Attributes["groups"].([]any)
	if len(gotGroups) != 1 || gotGroups[0] != "docker" {
		t.Errorf("nginx.home groups = %v, want [docker]", gotGroups)
	}
}

func TestMatchBlockTagsPopulateEnvoyConfig(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
resources:
  - name: noop
    type: exec
    command: "true"
`)

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "workstation.example.com"
    configcrates: [base]
    tags: [workstation, dev]
  - match: "*.media.example.com"
    configcrates: [base]
    tags: [mediaserver]
  - match: "plain.example.com"
    configcrates: [base]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("workstation.example.com")
	if got, want := cfg.Tags, []string{"workstation", "dev"}; !equalStrings(got, want) {
		t.Errorf("workstation tags = %v, want %v", got, want)
	}

	cfg = store.Get("tv.media.example.com")
	if got, want := cfg.Tags, []string{"mediaserver"}; !equalStrings(got, want) {
		t.Errorf("media tags = %v, want %v", got, want)
	}

	cfg = store.Get("plain.example.com")
	if len(cfg.Tags) != 0 {
		t.Errorf("plain tags = %v, want empty", cfg.Tags)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLoadConfigcrateAndResolve(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
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
    configcrates: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("test.example.com")
	if cfg.Envoy != "test.example.com" {
		t.Errorf("Envoy = %q, want test.example.com", cfg.Envoy)
	}
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("len(Configcrates) = %d, want 1", len(cfg.Configcrates))
	}
	if cfg.Configcrates[0].Name != "nginx" {
		t.Errorf("Module name = %q, want nginx", cfg.Configcrates[0].Name)
	}
}

func TestRoleResolution(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "configcrates", "certbot.vgo"), `
name: certbot
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present
`)
	writeRoles(t, dir, `
- name: webserver
  configcrates: [nginx, certbot]
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
	if len(cfg.Configcrates) != 2 {
		t.Fatalf("len(Configcrates) = %d, want 2", len(cfg.Configcrates))
	}
	if cfg.Configcrates[0].Name != "nginx" {
		t.Errorf("Module[0] = %q, want nginx", cfg.Configcrates[0].Name)
	}
	if cfg.Configcrates[1].Name != "certbot" {
		t.Errorf("Module[1] = %q, want certbot", cfg.Configcrates[1].Name)
	}
}

func TestConfigcrateDefaultVarsOverriddenByClient(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
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
    configcrates: [nginx]
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

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources:
  - name: config
    type: file
    state: present
    target_path: /etc/app/db.conf
    content: "secret:myapp/db/config"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web.example.com"
    environment: production
    configcrates: [app]
    vars:
      db_dsn: "secret:vigo/db/dsn"
`)

	mp := &mockProvider{secrets: map[string]string{
		"vigo/db/dsn":     "/srv/vigo/db/vigo.db",
		"myapp/db/config": "host=db port=5432 user=myapp",
	}}
	store, err := NewConfigStore(dir, mp, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	// Secret values are NOT resolved into the EnvoyConfig — placeholders
	// flow through to the agent which fetches actual values via the
	// ResolveSecrets RPC. Assert the placeholder shape + plaintext leak
	// absence.
	wantDSN := "__VIGO_SECRET__vigo/db/dsn__END_VIGO_SECRET__"
	if cfg.Vars["db_dsn"] != wantDSN {
		t.Errorf("db_dsn = %q, want placeholder %q", cfg.Vars["db_dsn"], wantDSN)
	}
	if dsn, _ := cfg.Vars["db_dsn"].(string); strings.Contains(dsn, "/srv/vigo/db/vigo.db") {
		t.Errorf("db_dsn leaks plaintext: %q", dsn)
	}
	wantContent := "__VIGO_SECRET__myapp/db/config__END_VIGO_SECRET__"
	if cnt, _ := cfg.Configcrates[0].Resources[0].Attributes["content"].(string); cnt != wantContent {
		t.Errorf("content = %q, want %q", cnt, wantContent)
	}
}

func TestKnownSecretEnforcement(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "bad.example.com"
    environment: production
    configcrates: [app]
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

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "fail.example.com"
    environment: production
    configcrates: [app]
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

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources:
  - name: config-file
    type: file
    state: present
    path: /etc/app/config
    content: "secret:app/config/content"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web-*.prod.example.com"
    environment: production
    configcrates: [app]
    vars:
      db_conn: "secret:missing/db/conn"
      api_key: "secret:missing/api/key"
  - match: "db-*.prod.example.com"
    environment: production
    configcrates: [app]
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
		{"local not found", "local", fmt.Errorf("secret %q not found in /srv/vigo/secrets", "x"), "vigocli secrets set"},
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

	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
resources: []
`)
	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*.prod.example.com"
    environment: production
    configcrates: [base, nginx]
  - match: "*.staging.example.com"
    environment: staging
    configcrates: [base]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.prod.example.com")
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want production", cfg.Environment)
	}
	if len(cfg.Configcrates) != 2 {
		t.Fatalf("len(Configcrates) = %d, want 2", len(cfg.Configcrates))
	}

	cfg2 := store.Get("app.staging.example.com")
	if cfg2.Environment != "staging" {
		t.Errorf("Environment = %q, want staging", cfg2.Environment)
	}
}

func TestFirstMatchWins(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "explicit.vgo"), `
name: explicit
resources: []
`)
	writeFile(t, filepath.Join(dir, "configcrates", "pattern.vgo"), `
name: pattern
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web01.prod.example.com"
    environment: staging
    configcrates: [explicit]
  - match: "*.prod.example.com"
    environment: production
    configcrates: [pattern]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.prod.example.com")
	if len(cfg.Configcrates) != 1 || cfg.Configcrates[0].Name != "explicit" {
		t.Errorf("expected explicit module, got %v", cfg.Configcrates)
	}
	if cfg.Environment != "staging" {
		t.Errorf("Environment = %q, want staging (first match)", cfg.Environment)
	}
}

func TestMultipleClientFiles(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "configcrates", "postgres.vgo"), `
name: postgres
resources: []
`)
	// Two separate client files
	writeFile(t, filepath.Join(dir, "default", "webservers.vgo"), `
envoys:
  - match: "web-*"
    environment: production
    configcrates: [nginx]
`)
	writeFile(t, filepath.Join(dir, "default", "databases.vgo"), `
envoys:
  - match: "db-*"
    environment: production
    configcrates: [postgres]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	web := store.Get("web-01")
	if len(web.Configcrates) != 1 || web.Configcrates[0].Name != "nginx" {
		t.Errorf("web-01: expected nginx, got %v", web.Configcrates)
	}

	db := store.Get("db-01")
	if len(db.Configcrates) != 1 || db.Configcrates[0].Name != "postgres" {
		t.Errorf("db-01: expected postgres, got %v", db.Configcrates)
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
	if len(cfg.Configcrates) != 0 {
		t.Errorf("expected empty modules, got %d", len(cfg.Configcrates))
	}
}

func TestConfigReload(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
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
    configcrates: [app]
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

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "good.example.com"
    environment: production
    configcrates: [app]
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

func TestUnknownConfigcrateError(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "bad.example.com"
    configcrates: [nonexistent]
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

func TestRoleReferencesUnknownConfigcrate(t *testing.T) {
	dir := setupDir(t)

	writeRoles(t, dir, `
- name: bad
  configcrates: [nonexistent]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for role referencing unknown module, got nil")
	}
}

func TestDedupConfigcrates(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeRoles(t, dir, `
- name: web
  configcrates: [nginx]
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "dup.example.com"
    roles: [web]
    configcrates: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("dup.example.com")
	if len(cfg.Configcrates) != 1 {
		t.Errorf("expected 1 module (deduped), got %d", len(cfg.Configcrates))
	}
}

func TestNodesReferencingSecret(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources:
  - name: config
    type: file
    state: present
    target_path: /etc/app/config
    content: "secret:myapp/db/config"
`)
	writeFile(t, filepath.Join(dir, "configcrates", "other.vgo"), `
name: other
resources: []
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web-*"
    configcrates: [app]
  - match: "db-*"
    configcrates: [other]
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

// TestCompoundSecretRefDetection is the regression for the rotation-detection
// drift: a secret referenced only *inside* a compound value (not the whole
// value) was substituted by resolveSecretsInValue but invisible to SecretPaths
// and EnvoysReferencingSecret — so the rotation watcher never polled it and
// never force-pushed on rotation, leaving a rotated credential live. Both
// detectors now route through the same scanner as substitution.
func TestCompoundSecretRefDetection(t *testing.T) {
	dir := setupDir(t)

	// The db_password secret appears embedded in a compound env value, not as
	// the whole attribute — exactly the shape the old HasPrefix/== checks missed.
	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources:
  - name: config
    type: file
    state: present
    target_path: /etc/app/config
    content: "DB_URL=postgres://app:secret:myapp/db/password@localhost/app,POOL=10"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web-*"
    configcrates: [app]
`)

	mp := &mockProvider{secrets: map[string]string{
		"myapp/db/password": "resolved-value",
	}}
	store, err := NewConfigStore(dir, mp, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// SecretPaths (the watcher's poll list) must include the embedded ref.
	paths := store.SecretPaths()
	found := false
	for _, p := range paths {
		if p == "myapp/db/password" {
			found = true
		}
	}
	if !found {
		t.Errorf("SecretPaths() missed the compound-embedded ref; got %v", paths)
	}

	// EnvoysReferencingSecret (the force-push targeting) must match it too.
	patterns := store.EnvoysReferencingSecret("myapp/db/password")
	if len(patterns) != 1 || patterns[0] != "web-*" {
		t.Errorf("EnvoysReferencingSecret missed the compound-embedded ref; got %v", patterns)
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

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
vars:
  worker_processes: "auto"
resources:
  - name: nginx-conf
    type: file
    state: present
    target_path: /etc/nginx/nginx.conf
    source: templates/nginx.conf.tmpl
    owner: root
    mode: "0644"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web.example.com"
    configcrates: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("expected 1 module, got %d", len(cfg.Configcrates))
	}

	res := cfg.Configcrates[0].Resources[0]
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

	writeFile(t, filepath.Join(dir, "configcrates", "bad.vgo"), `
name: bad
resources:
  - name: both-set
    type: file
    state: present
    target_path: /etc/test
    source: templates/test.tmpl
    content: "inline content"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "test.example.com"
    configcrates: [bad]
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

	writeFile(t, filepath.Join(dir, "configcrates", "bad.vgo"), `
name: bad
resources:
  - name: missing-source
    type: file
    state: present
    target_path: /etc/test
    source: templates/nonexistent.tmpl
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "test.example.com"
    configcrates: [bad]
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

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
vars:
  port: "80"
resources:
  - name: nginx-site
    type: file
    state: present
    target_path: /etc/nginx/sites-enabled/default
    source: templates/nginx/sites/default.conf.tmpl
    mode: "0644"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web.example.com"
    configcrates: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web.example.com")
	res := cfg.Configcrates[0].Resources[0]
	content, ok := res.Attributes["content"]
	if !ok {
		t.Fatal("expected 'content' attribute after source resolution")
	}
	if content != `server { listen {{ .Vars.port }}; }` {
		t.Errorf("content = %q, want template text", content)
	}
}

func TestLoadConfigcrateConditionalBlockLoadsCorrectly(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "debian-tools.vgo"), `
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
    configcrates: [debian-tools]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("deb.example.com")
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("len(Configcrates) = %d, want 1", len(cfg.Configcrates))
	}
	mod := cfg.Configcrates[0]
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

func TestLoadConfigcrateMixedPlainAndConditionalBlock(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "mixed.vgo"), `
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
    configcrates: [mixed]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("mix.example.com")
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("len(Configcrates) = %d, want 1", len(cfg.Configcrates))
	}
	mod := cfg.Configcrates[0]
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

func TestLoadConfigcrateConditionalBlockDependsOnAcrossBoundary(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "cross-dep.vgo"), `
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
    configcrates: [cross-dep]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("dep.example.com")
	mod := cfg.Configcrates[0]
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

func TestLoadConfigcrateConditionalBlockWithResourceLevelConditionals(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "dual-cond.vgo"), `
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
    configcrates: [dual-cond]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("dual.example.com")
	mod := cfg.Configcrates[0]
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

func TestLoadConfigcrateConditionalBlockEmptyResourcesError(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "bad-block.vgo"), `
name: bad-block
resources:
  - conditional_block:
      when: "is_debian"
      resources: []
`)

	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "bad.example.com"
    configcrates: [bad-block]
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

func TestRoleWithConditionalConfigcrateRef(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "configcrates", "certbot.vgo"), `
name: certbot
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present
`)
	writeRoles(t, dir, `
- name: webserver
  configcrates:
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
	if len(cfg.Configcrates) != 2 {
		t.Fatalf("len(Configcrates) = %d, want 2", len(cfg.Configcrates))
	}

	// nginx should have When from the role ref.
	if cfg.Configcrates[0].When != "is_debian" {
		t.Errorf("nginx.When = %q, want \"is_debian\"", cfg.Configcrates[0].When)
	}

	// certbot should have no conditionals.
	if cfg.Configcrates[1].When != "" {
		t.Errorf("certbot.When = %q, want empty", cfg.Configcrates[1].When)
	}
}

func TestClientWithConditionalConfigcrateRef(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "redis.vgo"), `
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
    configcrates:
      - name: redis
        when: "not is_container"
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("db.example.com")
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("len(Configcrates) = %d, want 1", len(cfg.Configcrates))
	}
	if cfg.Configcrates[0].When != "not is_container" {
		t.Errorf("redis.When = %q, want \"not is_container\"", cfg.Configcrates[0].When)
	}
}

func TestMixedPlainAndConditionalConfigcrateRefsInRole(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "configcrates", "certbot.vgo"), `
name: certbot
resources: []
`)
	writeRoles(t, dir, `
- name: web
  configcrates:
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
	if len(cfg.Configcrates) != 2 {
		t.Fatalf("len(Configcrates) = %d, want 2", len(cfg.Configcrates))
	}
	// nginx: plain string, no conditionals
	if cfg.Configcrates[0].When != "" {
		t.Errorf("nginx should have no conditionals, got When=%q", cfg.Configcrates[0].When)
	}
	// certbot: object with when expression
	if cfg.Configcrates[1].When != "is_debian or is_ubuntu" {
		t.Errorf("certbot.When = %q, want \"is_debian or is_ubuntu\"", cfg.Configcrates[1].When)
	}
}

func TestExcludeResources(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "hardening.vgo"), `
name: hardening
resources:
  - name: disable-xserver
    type: package
    package: xserver-xorg
    state: absent
  - name: disable-avahi
    type: service
    service: avahi-daemon
    state: stopped
    enabled: false
  - name: kernel-aslr
    type: sysctl
    state: present
    key: kernel.randomize_va_space
    value: "2"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "desktop.example.com"
    configcrates: [hardening]
    exclude_resources:
      - disable-xserver
      - disable-avahi
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("desktop.example.com")
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("len(Configcrates) = %d, want 1", len(cfg.Configcrates))
	}
	// Only kernel-aslr should remain.
	if len(cfg.Configcrates[0].Resources) != 1 {
		t.Fatalf("len(Resources) = %d, want 1", len(cfg.Configcrates[0].Resources))
	}
	if cfg.Configcrates[0].Resources[0].Name != "kernel-aslr" {
		t.Errorf("remaining resource = %q, want kernel-aslr", cfg.Configcrates[0].Resources[0].Name)
	}
}

func TestConditionalRoleRef(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "configcrates", "sshd.vgo"), `
name: sshd
resources: []
`)
	writeFile(t, filepath.Join(dir, "configcrates", "iis.vgo"), `
name: iis
resources: []
`)
	writeRoles(t, dir, `
- name: linux-web
  configcrates: [nginx, sshd]
`, `
- name: windows-web
  configcrates: [iis]
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
	if len(cfg.Configcrates) != 3 {
		t.Fatalf("len(Configcrates) = %d, want 3", len(cfg.Configcrates))
	}
	// nginx and sshd should inherit the role's when condition.
	if cfg.Configcrates[0].When != "os_family('linux')" {
		t.Errorf("nginx.When = %q, want \"os_family('linux')\"", cfg.Configcrates[0].When)
	}
	if cfg.Configcrates[1].When != "os_family('linux')" {
		t.Errorf("sshd.When = %q, want \"os_family('linux')\"", cfg.Configcrates[1].When)
	}
	// iis should inherit the windows role's when condition.
	if cfg.Configcrates[2].When != "os_family('windows')" {
		t.Errorf("iis.When = %q, want \"os_family('windows')\"", cfg.Configcrates[2].When)
	}
	// EnvoyConfig.Roles should list all role names.
	if len(cfg.Roles) != 2 || cfg.Roles[0] != "linux-web" || cfg.Roles[1] != "windows-web" {
		t.Errorf("Roles = %v, want [linux-web windows-web]", cfg.Roles)
	}
}

func TestConditionalRoleRefPreservesConfigcrateWhen(t *testing.T) {
	// When both the role ref AND the module ref have when: conditions,
	// the module's own condition should take precedence.
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources: []
`)
	writeFile(t, filepath.Join(dir, "configcrates", "certbot.vgo"), `
name: certbot
resources: []
`)
	writeRoles(t, dir, `
- name: web
  configcrates:
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
	if len(cfg.Configcrates) != 2 {
		t.Fatalf("len(Configcrates) = %d, want 2", len(cfg.Configcrates))
	}
	// nginx has no module-level when, so it inherits the role's condition.
	if cfg.Configcrates[0].When != "os_family('linux')" {
		t.Errorf("nginx.When = %q, want \"os_family('linux')\"", cfg.Configcrates[0].When)
	}
	// certbot has its own when: condition, so role's condition should NOT override it.
	if cfg.Configcrates[1].When != "file_exists('/usr/bin/certbot')" {
		t.Errorf("certbot.When = %q, want \"file_exists('/usr/bin/certbot')\"", cfg.Configcrates[1].When)
	}
}

func TestBackwardCompatPlainStringConfigcratesStillWork(t *testing.T) {
	// This test explicitly verifies that existing role/client YAML with plain
	// string module references continues to load without any changes.
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "configcrates", "certbot.vgo"), `
name: certbot
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present
`)
	writeRoles(t, dir, `
- name: webserver
  configcrates: [nginx, certbot]
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web01.example.com"
    roles: [webserver]
  - match: "web02.example.com"
    configcrates: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg1 := store.Get("web01.example.com")
	if len(cfg1.Configcrates) != 2 {
		t.Fatalf("web01: len(Configcrates) = %d, want 2", len(cfg1.Configcrates))
	}
	if cfg1.Configcrates[0].Name != "nginx" || cfg1.Configcrates[1].Name != "certbot" {
		t.Errorf("web01: modules = [%s, %s], want [nginx, certbot]", cfg1.Configcrates[0].Name, cfg1.Configcrates[1].Name)
	}

	cfg2 := store.Get("web02.example.com")
	if len(cfg2.Configcrates) != 1 {
		t.Fatalf("web02: len(Configcrates) = %d, want 1", len(cfg2.Configcrates))
	}
	if cfg2.Configcrates[0].Name != "nginx" {
		t.Errorf("web02: module = %q, want nginx", cfg2.Configcrates[0].Name)
	}
}

func TestIdempotencyValidation_BlocksUnguardedExec(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "bad.vgo"), `
name: bad
resources:
  - name: yolo-exec
    type: exec
    command: "longdrawer hello"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "test.example.com"
    configcrates: [bad]
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
	if stats.Configcrates != 0 {
		t.Errorf("expected 0 modules loaded, got %d", stats.Configcrates)
	}
}

func TestIdempotencyValidation_DangerousModeAllowsUnguardedExec(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "bad.vgo"), `
name: bad
resources:
  - name: yolo-exec
    type: exec
    command: "longdrawer hello"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "test.example.com"
    configcrates: [bad]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("dangerous mode should allow unguarded exec, got: %v", err)
	}
	cfg := store.Get("test.example.com")
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("expected 1 module, got %d", len(cfg.Configcrates))
	}
}

func TestIdempotencyValidation_GuardedExecPasses(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "good.vgo"), `
name: good
resources:
  - name: guarded-exec
    type: exec
    command: "touch /tmp/done"
    unless: "test -f /tmp/done"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "test.example.com"
    configcrates: [good]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("guarded exec should pass validation, got: %v", err)
	}
}

func TestVgoFileExtension_LoadedByConfigStore(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeRoles(t, dir, `
- name: web
  configcrates: [nginx]
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
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("expected 1 module, got %d", len(cfg.Configcrates))
	}
	if cfg.Configcrates[0].Name != "nginx" {
		t.Errorf("expected module nginx, got %q", cfg.Configcrates[0].Name)
	}
}

func TestMultipleIdempotencyErrorsCollected(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "bad1.vgo"), `
name: bad1
resources:
  - name: yolo-exec
    type: exec
    command: "longdrawer one"
`)
	writeFile(t, filepath.Join(dir, "configcrates", "bad2.vgo"), `
name: bad2
resources:
  - name: another-yolo
    type: exec
    command: "longdrawer two"
  - name: third-yolo
    type: exec
    command: "longdrawer three"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "test.example.com"
    configcrates: [bad1, bad2]
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
	if len(ie.Configcrates) != 2 {
		t.Errorf("expected 2 modules with errors, got %d", len(ie.Configcrates))
	}

	// bad2 has 2 unguarded execs.
	totalFindings := 0
	for _, m := range ie.Configcrates {
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
	if stats.Configcrates != 0 {
		t.Errorf("expected 0 modules loaded, got %d", stats.Configcrates)
	}
}

func TestDuplicateConfigcratesKeysMerged(t *testing.T) {
	dir := setupDir(t)

	for _, name := range []string{"docker", "grafana", "envoy-exporter"} {
		writeFile(t, filepath.Join(dir, "configcrates", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    package: %s
    state: present
`, name, name, name))
	}
	// Duplicate configcrates: keys — should merge, not overwrite.
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "danlap"
    environment: production
    configcrates: [docker]
    configcrates: [grafana]
    configcrates: [envoy-exporter]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, false)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("danlap")
	if len(cfg.Configcrates) != 3 {
		names := make([]string, len(cfg.Configcrates))
		for i, m := range cfg.Configcrates {
			names[i] = m.Name
		}
		t.Fatalf("expected 3 modules, got %d: %v", len(cfg.Configcrates), names)
	}

	want := []string{"docker", "grafana", "envoy-exporter"}
	for i, m := range cfg.Configcrates {
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
		writeFile(t, filepath.Join(dir, "configcrates", name+".vgo"), fmt.Sprintf(`
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
  configcrates: [nginx]
`, `
- name: cache
  configcrates: [redis]
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
	if len(cfg.Configcrates) != 2 {
		t.Fatalf("expected 2 modules, got %d", len(cfg.Configcrates))
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

	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base-packages
before: [app-config]
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`)
	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
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
    configcrates: [base-packages, app-config]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("app.example.com")
	if len(cfg.Configcrates) != 2 {
		t.Fatalf("len(Configcrates) = %d, want 2", len(cfg.Configcrates))
	}

	// base-packages has before: [app-config], so app-config should have
	// depends_on: [base-packages] after resolution.
	appMod := cfg.Configcrates[1]
	if appMod.Name != "app-config" {
		t.Fatalf("Module[1] = %q, want app-config", appMod.Name)
	}
	if len(appMod.DependsOn) != 1 || appMod.DependsOn[0] != "base-packages" {
		t.Errorf("app-config DependsOn = %v, want [base-packages]", appMod.DependsOn)
	}

	// base-packages should have no depends_on.
	baseMod := cfg.Configcrates[0]
	if len(baseMod.DependsOn) != 0 {
		t.Errorf("base-packages DependsOn = %v, want []", baseMod.DependsOn)
	}
}

func TestBeforeUnknownTargetErrors(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
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
    configcrates: [base-packages]
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
	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base-packages
before: [app-config]
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`)
	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
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
    configcrates: [base-packages, app-config]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("app.example.com")
	appMod := cfg.Configcrates[1]
	if len(appMod.DependsOn) != 1 {
		t.Errorf("expected 1 dependency, got %d: %v", len(appMod.DependsOn), appMod.DependsOn)
	}
}

// --- vars_from tests ---

func TestOverlappingGlobFirstWins(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources:
  - name: install
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "configcrates", "apache.vgo"), `
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
    configcrates: [nginx]
  - match: "*.prod.example.com"
    configcrates: [apache]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.prod.example.com")
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("len(Configcrates) = %d, want 1", len(cfg.Configcrates))
	}
	if cfg.Configcrates[0].Name != "nginx" {
		t.Errorf("Module = %q, want nginx (first glob should win)", cfg.Configcrates[0].Name)
	}

	// db01 should match the second pattern.
	cfg2 := store.Get("db01.prod.example.com")
	if len(cfg2.Configcrates) != 1 {
		t.Fatalf("db01: len(Configcrates) = %d, want 1", len(cfg2.Configcrates))
	}
	if cfg2.Configcrates[0].Name != "apache" {
		t.Errorf("db01 Module = %q, want apache", cfg2.Configcrates[0].Name)
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
	if len(cfg.Configcrates) != 0 {
		t.Errorf("expected 0 modules for empty config, got %d", len(cfg.Configcrates))
	}
}

func TestNodeVarsOverrideConfigcrateDefaults(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "webserver.vgo"), `
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
    configcrates: [webserver]
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

func TestSourcePathTraversalDotDot(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources:
  - name: conf
    type: file
    state: present
    target_path: /etc/app/config
    source: "../../etc/shadow"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web01.example.com"
    configcrates: [app]
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

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources:
  - name: conf
    type: file
    state: present
    target_path: /etc/app/config
    source: "/etc/passwd"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web01.example.com"
    configcrates: [app]
`)

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for absolute source path")
	}
}

func TestConcurrentGetDuringReload(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
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
    configcrates: [nginx]
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

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
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
    configcrates: [nginx]
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

	// Reload also stamps the new version's mint time for publish→converge
	// measurement.
	if _, ok := store.VersionMintedAt(v2); !ok {
		t.Errorf("expected version %d to have a recorded mint time after reload", v2)
	}
}

func TestVersionMintedAtHorizon(t *testing.T) {
	cs := &ConfigStore{}
	base := time.Now()

	cs.recordVersionMint(1, base)
	if _, ok := cs.VersionMintedAt(1); !ok {
		t.Fatal("version 1 should be retrievable immediately after minting")
	}
	if _, ok := cs.VersionMintedAt(99); ok {
		t.Error("an unminted version should not be retrievable")
	}

	// Minting a later version far in the future prunes the stale entry.
	cs.recordVersionMint(2, base.Add(versionMintHorizon+time.Minute))
	if _, ok := cs.VersionMintedAt(1); ok {
		t.Error("version 1 should be pruned once older than the retention horizon")
	}
	if t2, ok := cs.VersionMintedAt(2); !ok || !t2.After(base) {
		t.Error("version 2 should remain retrievable")
	}
}

func TestReloadFailureKeepsOldConfig(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
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
    configcrates: [nginx]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("web01.example.com")
	if len(cfg.Configcrates) != 1 {
		t.Fatalf("expected 1 module, got %d", len(cfg.Configcrates))
	}

	// Write invalid module reference.
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [nonexistent_configcrate_xyz]
`)

	// Reload should fail but old config preserved.
	reloadErr := store.Reload()
	if reloadErr == nil {
		// Some configs may allow missing modules; either way, the old config should remain valid.
		return
	}

	// After failed reload, Get() should still return the old valid config.
	cfg = store.Get("web01.example.com")
	if len(cfg.Configcrates) != 1 || cfg.Configcrates[0].Name != "nginx" {
		t.Error("expected old config to be preserved after failed reload")
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
	writeFile(t, filepath.Join(dir, "configcrates", "evil.vgo"), `
name: evil
resources:
  - name: evil-file
    type: file
    state: present
    target_path: /etc/evil.conf
    source: templates/evil.conf
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "test.example.com"
    configcrates: [evil]
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

	writeFile(t, filepath.Join(dir, "configcrates", "firewall.vgo"), `
name: firewall
resources:
  - name: fw-rules
    type: file
    state: present
    target_path: /etc/iptables/rules.v4
`)
	writeFile(t, filepath.Join(dir, "configcrates", "sshd.vgo"), `
name: sshd
resources:
  - name: sshd-config
    type: file
    state: present
    target_path: /etc/ssh/sshd_config
`)
	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeRoles(t, dir, `
- name: base-security
  configcrates:
    - firewall
    - sshd
`, `
- name: webserver
  includes:
    - base-security
  configcrates:
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
	if len(cfg.Configcrates) != 3 {
		t.Fatalf("len(Configcrates) = %d, want 3", len(cfg.Configcrates))
	}
	if cfg.Configcrates[0].Name != "firewall" {
		t.Errorf("Configcrates[0] = %q, want firewall", cfg.Configcrates[0].Name)
	}
	if cfg.Configcrates[1].Name != "sshd" {
		t.Errorf("Configcrates[1] = %q, want sshd", cfg.Configcrates[1].Name)
	}
	if cfg.Configcrates[2].Name != "nginx" {
		t.Errorf("Configcrates[2] = %q, want nginx", cfg.Configcrates[2].Name)
	}
}

func TestRoleIncludesNestedRejected(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "a.vgo"), `
name: a
resources:
  - name: r
    type: file
    state: present
    target_path: /tmp/a
`)
	writeRoles(t, dir, `
- name: inner
  includes:
    - outer
  configcrates:
    - a
`, `
- name: outer
  includes:
    - inner
  configcrates:
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

	writeFile(t, filepath.Join(dir, "configcrates", "shared.vgo"), `
name: shared
resources:
  - name: r
    type: file
    state: present
    target_path: /tmp/shared
`)
	writeFile(t, filepath.Join(dir, "configcrates", "web.vgo"), `
name: web
resources:
  - name: r
    type: file
    state: present
    target_path: /tmp/web
`)
	writeRoles(t, dir, `
- name: base
  configcrates:
    - shared
`, `
- name: webserver
  includes:
    - base
  configcrates:
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
	if len(cfg.Configcrates) != 2 {
		t.Fatalf("len(Configcrates) = %d, want 2", len(cfg.Configcrates))
	}
	if cfg.Configcrates[0].Name != "shared" {
		t.Errorf("Configcrates[0] = %q, want shared", cfg.Configcrates[0].Name)
	}
	if cfg.Configcrates[1].Name != "web" {
		t.Errorf("Configcrates[1] = %q, want web", cfg.Configcrates[1].Name)
	}
}

// --- Wildcard vars_from ---

// --- Config Trace ---

func TestConfigTrace(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
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
    state: present
    target_path: /etc/nginx/nginx.conf
`)
	writeFile(t, filepath.Join(dir, "configcrates", "firewall.vgo"), `
name: firewall
resources:
  - name: fw
    type: file
    state: present
    target_path: /etc/iptables/rules.v4
`)
	writeRoles(t, dir, `
- name: base
  configcrates:
    - firewall
`, `
- name: web
  includes:
    - base
  configcrates:
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
	if len(trace.Configcrates) != 2 {
		t.Fatalf("len(Configcrates) = %d, want 2", len(trace.Configcrates))
	}
	if trace.Configcrates[0] != "firewall" || trace.Configcrates[1] != "nginx" {
		t.Errorf("Configcrates = %v, want [firewall nginx]", trace.Configcrates)
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
	writeFile(t, filepath.Join(dir, "configcrates", "a.vgo"), `
name: a
resources:
  - name: r
    type: file
    state: present
    target_path: /tmp/a
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "other"
    configcrates: [a]
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

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources:
  - name: r
    type: file
    state: present
    target_path: /tmp/app
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web-*.example.com"
    configcrates: [app]
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
	dir := filepath.Join(t.TempDir(), "config", "stacks")
	for _, sub := range []string{"configcrates", "usercrates", ""} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInheritance_BasicConfigcrates(t *testing.T) {
	dir := setupInheritanceDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "sshd.vgo"), `
name: sshd
resources:
  - name: install-sshd
    type: package
    state: present
    package: openssh-server
`)
	writeFile(t, filepath.Join(dir, "configcrates", "monitoring.vgo"), `
name: monitoring
resources:
  - name: install-monitoring
    type: package
    state: present
    package: prometheus-node-exporter
`)
	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    state: present
    package: nginx
`)

	// Root common.vgo — inherited by all subdirs.
	writeFile(t, filepath.Join(dir, "common.vgo"), `
configcrates: [sshd, monitoring]
`)

	// Leaf entry in a subdirectory.
	os.MkdirAll(filepath.Join(dir, "web"), 0o755)
	writeFile(t, filepath.Join(dir, "web", "web.vgo"), `
envoys:
  - match: "web*.example.com"
    configcrates: [nginx]
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
	var configcrateNames []string
	for _, m := range cfg.Configcrates {
		configcrateNames = append(configcrateNames, m.Name)
	}

	if len(configcrateNames) != 3 {
		t.Fatalf("expected 3 modules, got %d: %v", len(configcrateNames), configcrateNames)
	}
	// Parent modules first, then own.
	if configcrateNames[0] != "sshd" {
		t.Errorf("module[0] = %q, want sshd", configcrateNames[0])
	}
	if configcrateNames[1] != "monitoring" {
		t.Errorf("module[1] = %q, want monitoring", configcrateNames[1])
	}
	if configcrateNames[2] != "nginx" {
		t.Errorf("module[2] = %q, want nginx", configcrateNames[2])
	}
}

func TestInheritance_MultiLevel(t *testing.T) {
	dir := setupInheritanceDir(t)

	for _, name := range []string{"sshd", "logging", "nginx"} {
		writeFile(t, filepath.Join(dir, "configcrates", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    state: present
    package: %s
`, name, name, name))
	}

	writeFile(t, filepath.Join(dir, "common.vgo"), `
configcrates: [sshd]
`)

	os.MkdirAll(filepath.Join(dir, "production"), 0o755)
	writeFile(t, filepath.Join(dir, "production", "common.vgo"), `
configcrates: [logging]
`)

	os.MkdirAll(filepath.Join(dir, "production", "web"), 0o755)
	writeFile(t, filepath.Join(dir, "production", "web", "web.vgo"), `
envoys:
  - match: "web*.prod.example.com"
    configcrates: [nginx]
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
	for _, m := range cfg.Configcrates {
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

func TestInheritance_ExcludeConfigcrates(t *testing.T) {
	dir := setupInheritanceDir(t)

	for _, name := range []string{"sshd", "monitoring", "nginx"} {
		writeFile(t, filepath.Join(dir, "configcrates", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    state: present
    package: %s
`, name, name, name))
	}

	writeFile(t, filepath.Join(dir, "common.vgo"), `
configcrates: [sshd, monitoring]
`)

	os.MkdirAll(filepath.Join(dir, "containers"), 0o755)
	writeFile(t, filepath.Join(dir, "containers", "containers.vgo"), `
envoys:
  - match: "docker*.example.com"
    configcrates: [nginx]
    exclude_configcrates: [monitoring]
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
	for _, m := range cfg.Configcrates {
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

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources:
  - name: install-app
    type: package
    state: present
    package: app
`)

	writeFile(t, filepath.Join(dir, "common.vgo"), `
configcrates: [app]
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

	writeFile(t, filepath.Join(dir, "configcrates", "sshd.vgo"), `
name: sshd
resources:
  - name: install-sshd
    type: package
    state: present
    package: openssh-server
`)
	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources:
  - name: install-app
    type: package
    state: present
    package: myapp
`)

	writeFile(t, filepath.Join(dir, "common.vgo"), `
configcrates: [sshd]
`)

	os.MkdirAll(filepath.Join(dir, "servers"), 0o755)
	writeFile(t, filepath.Join(dir, "servers", "all.vgo"), `
envoys:
  - match: "srv*.example.com"
    configcrates: [app]
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
	for _, m := range cfg.Configcrates {
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
		writeFile(t, filepath.Join(dir, "configcrates", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    state: present
    package: %s
`, name, name, name))
	}

	writeFile(t, filepath.Join(dir, "common.vgo"), `
configcrates: [sshd]
`)

	os.MkdirAll(filepath.Join(dir, "web"), 0o755)
	writeFile(t, filepath.Join(dir, "web", "web.vgo"), `
envoys:
  - match: "web*.example.com"
    configcrates: [nginx]
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
	if len(trace.Inheritance[0].Configcrates) != 1 || trace.Inheritance[0].Configcrates[0] != "sshd" {
		t.Errorf("inheritance[0].Configcrates = %v, want [sshd]", trace.Inheritance[0].Configcrates)
	}
	// Leaf entry level shows the source file.
	if trace.Inheritance[1].File != "web.vgo" {
		t.Errorf("inheritance[1].File = %q, want web.vgo", trace.Inheritance[1].File)
	}
	if trace.Inheritance[1].Dir != "web" {
		t.Errorf("inheritance[1].Dir = %q, want web", trace.Inheritance[1].Dir)
	}
	if len(trace.Inheritance[1].Configcrates) != 1 || trace.Inheritance[1].Configcrates[0] != "nginx" {
		t.Errorf("inheritance[1].Configcrates = %v, want [nginx]", trace.Inheritance[1].Configcrates)
	}
}

func TestInheritance_InheritedRoles(t *testing.T) {
	dir := setupInheritanceDir(t)

	for _, name := range []string{"sshd", "monitoring", "nginx"} {
		writeFile(t, filepath.Join(dir, "configcrates", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    state: present
    package: %s
`, name, name, name))
	}

	// roles.vgo at root.
	writeFile(t, filepath.Join(dir, "roles.vgo"), `
roles:
  - name: base-security
    configcrates: [sshd]
`)

	writeFile(t, filepath.Join(dir, "common.vgo"), `
roles: [base-security]
configcrates: [monitoring]
`)

	os.MkdirAll(filepath.Join(dir, "web"), 0o755)
	writeFile(t, filepath.Join(dir, "web", "web.vgo"), `
envoys:
  - match: "web*.example.com"
    configcrates: [nginx]
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
	for _, m := range cfg.Configcrates {
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

// --- New function tests: compliance index, when display, var priority ---

func TestBuildComplianceIndex(t *testing.T) {
	mods := map[string]*ConfigcrateDefinition{
		"sshd": {
			Name: "sshd",
			InheritedCompliance: map[string][]string{
				"hipaa":   {"AC-1", "SC-8"},
				"pci-dss": {"2.2.1", "8.3.1"},
			},
		},
		"nginx": {
			Name: "nginx",
			InheritedCompliance: map[string][]string{
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

	// Per-module subdirs with sibling compliance.vgo inheritance files —
	// the post-0.66.57 way to attribute specific framework→control IDs
	// to a single module (per-module `compliance:` carries the catalog-
	// tag shape only; the inheritance file keeps the framework→control
	// map shape).
	writeFile(t, filepath.Join(dir, "configcrates", "sshd", "sshd.vgo"), `
name: sshd
resources:
  - name: sshd-config
    type: file
    state: present
    target_path: /etc/ssh/sshd_config
`)
	writeFile(t, filepath.Join(dir, "configcrates", "sshd", "compliance.vgo"), `
compliance:
  hipaa: [AC-1, SC-8]
  pci-dss: ["2.2.1", "8.3.1"]
`)
	writeFile(t, filepath.Join(dir, "configcrates", "nginx", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "configcrates", "nginx", "compliance.vgo"), `
compliance:
  pci-dss: ["6.5.1"]
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    configcrates: [sshd, nginx]
  - match: "db01"
    configcrates: [sshd]
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
	if len(cov.CoveredControls) != 3 {
		t.Errorf("CoveredControls = %d, want 3", len(cov.CoveredControls))
	}

	// db01 has only sshd — should cover 2 of 3 pci-dss controls, missing 6.5.1.
	cov2 := store.EnvoyStandardCoverage("db01", "pci-dss")
	if cov2 == nil {
		t.Fatal("expected coverage for db01, got nil")
	}
	if len(cov2.CoveredControls) != 2 {
		t.Errorf("db01 CoveredControls = %d, want 2", len(cov2.CoveredControls))
	}
	for _, cc := range cov2.CoveredControls {
		if cc.ControlID == "6.5.1" {
			t.Errorf("db01 covers 6.5.1, which only nginx claims")
		}
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

	// Memoization contract: two consecutive lookups for the same
	// (hostname, standard) must return the same pointer. EnvoyStandardCoverage
	// is now an O(1) cache hit, not a fresh allocation per call.
	a := store.EnvoyStandardCoverage("web01", "pci-dss")
	b := store.EnvoyStandardCoverage("web01", "pci-dss")
	if a != b {
		t.Errorf("expected memoized pointer equality, got %p vs %p", a, b)
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

	writeFile(t, filepath.Join(dir, "configcrates", "db.vgo"), `
name: db
vars:
  dsn: "secret:vigo/db/dsn"
resources:
  - name: db-config
    type: file
    state: present
    target_path: /etc/db.conf
    content: "dsn={{ .Vars.dsn }}"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "db01"
    configcrates: [db]
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
		if sr.Key == "dsn" && sr.Path == "vigo/db/dsn" && sr.Configcrate == "db" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SecretRef for dsn, got %v", trace.SecretRefs)
	}
}

func TestTraceDependencies(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
name: nginx
depends_on: [sshd]
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
  - name: nginx-config
    type: file
    state: present
    target_path: /etc/nginx/nginx.conf
    notify: [nginx-service]
  - name: nginx-service
    type: service
    service: nginx
    state: running
    depends_on: [install-nginx]
`)
	writeFile(t, filepath.Join(dir, "configcrates", "sshd.vgo"), `
name: sshd
resources:
  - name: sshd-config
    type: file
    state: present
    target_path: /etc/ssh/sshd_config
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    configcrates: [sshd, nginx]
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
		if d.From == "nginx" && d.To == "sshd" && d.Type == "depends_on" && d.Level == "configcrate" {
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

	writeFile(t, filepath.Join(dir, "configcrates", "sshd", "sshd.vgo"), `
name: sshd
resources:
  - name: sshd-config
    type: file
    state: present
    target_path: /etc/ssh/sshd_config
`)
	writeFile(t, filepath.Join(dir, "configcrates", "sshd", "compliance.vgo"), `
compliance:
  hipaa: [AC-1, SC-8]
  pci-dss: ["2.2.1"]
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    configcrates: [sshd]
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
	if ct.Configcrate != "sshd" {
		t.Errorf("Module = %q, want sshd", ct.Configcrate)
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

	writeFile(t, filepath.Join(dir, "configcrates", "scanner.vgo"), `
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
    configcrates: [scanner]
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

	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
vars:
  port: "8080"
  log_level: "info"
resources:
  - name: app-config
    type: file
    state: present
    target_path: /etc/app.conf
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    configcrates: [app]
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

func TestFilterByConfigcrateFiltersNewSections(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "configcrates", "sshd", "sshd.vgo"), `
name: sshd
resources:
  - name: sshd-config
    type: file
    state: present
    target_path: /etc/ssh/sshd_config
    when: "is_enabled"
`)
	writeFile(t, filepath.Join(dir, "configcrates", "sshd", "compliance.vgo"), `
compliance:
  hipaa: [AC-1]
`)
	writeFile(t, filepath.Join(dir, "configcrates", "nginx", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
    when: "os_family('debian')"
`)
	writeFile(t, filepath.Join(dir, "configcrates", "nginx", "compliance.vgo"), `
compliance:
  pci-dss: ["6.5.1"]
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "web01"
    configcrates: [sshd, nginx]
    vars:
      is_enabled: true
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	trace := store.Trace("web01")
	filtered := trace.FilterByConfigcrate("nginx")

	// Compliance should only include nginx.
	if len(filtered.ComplianceTags) != 1 || filtered.ComplianceTags[0].Configcrate != "nginx" {
		t.Errorf("filtered ComplianceTags = %v, want [nginx]", filtered.ComplianceTags)
	}

	// Conditionals should only include nginx resources.
	for _, c := range filtered.Conditionals {
		if c.Configcrate != "nginx" {
			t.Errorf("filtered Conditional module = %q, want nginx", c.Configcrate)
		}
	}
}

// --- Pending-features test suite ---
//
// These tests pin the contracts described in pending.md but document
// behavior that isn't implemented yet. They use t.Skip so CI stays green;
// implementer removes the t.Skip line after wiring the feature.

// TestResolveConfigcrateSecrets_EmbeddedRefs_EmitsPlaceholders pins the post-
// placeholder behavior: embedded `secret:<key>` refs inside compound
// attribute values (env, environment, cron environment, container
// labels, etc.) are substituted with `__VIGO_SECRET__<key>__END_VIGO_SECRET__`
// markers. The provider is consulted to validate that the secret
// exists at load time, but the resolved plaintext is NOT stored in the
// EnvoyConfig — the agent fetches the actual value via the
// VigoAgent.ResolveSecrets RPC at apply time.
func TestResolveConfigcrateSecrets_EmbeddedRefs_EmitsPlaceholders(t *testing.T) {
	dir := setupDir(t)

	// container: resource with embedded secret in env; mirrors the
	// pattern the docs example shows.
	writeFile(t, filepath.Join(dir, "configcrates", "redis.vgo"), `
name: redis
resources:
  - name: redis-cache
    type: container
    container: redis-cache
    image: redis:7-alpine
    state: running
    env: "REDIS_PASSWORD=secret:vigo/redis/password,REDIS_DB=cache"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "redis-host"
    configcrates: [redis]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{
		"vigo/redis/password": "hunter2",
	}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	_, cfg := store.Match("redis-host")
	if cfg == nil {
		t.Fatal("Match(redis-host) returned nil config")
	}
	if len(cfg.Configcrates) == 0 || len(cfg.Configcrates[0].Resources) == 0 {
		t.Fatalf("expected one resource on the redis module, got %#v", cfg.Configcrates)
	}
	envAttr, _ := cfg.Configcrates[0].Resources[0].Attributes["env"].(string)

	want := "REDIS_PASSWORD=__VIGO_SECRET__vigo/redis/password__END_VIGO_SECRET__,REDIS_DB=cache"
	if envAttr != want {
		t.Errorf("env attribute = %q, want %q (embedded `secret:` ref must be substituted with placeholder)", envAttr, want)
	}
	if strings.Contains(envAttr, "hunter2") {
		t.Errorf("env attribute leaks plaintext secret value: %q", envAttr)
	}
}

// TestResolveConfigcrateSecrets_LiteralSecretColonInContent guards the inline
// secret scanner against literal `secret:` text in a file resource's content.
// The Prometheus alert-rules file (vigo-prometheus-alerts) ships a description
// `A `+"`"+`secret:`+"`"+` reference failed to resolve` — the backtick after
// `secret:` must NOT be parsed as a secret key. A real embedded reference in
// the same content must still resolve, proving the fix doesn't disable inline
// resolution.
func TestResolveConfigcrateSecrets_LiteralSecretColonInContent(t *testing.T) {
	dir := setupDir(t)

	// `content:` carries both the literal documentation text (must survive
	// verbatim) and a genuine embedded reference (must become a placeholder).
	const literal = "A `secret:` reference failed to resolve. Check the secrets provider health."
	writeFile(t, filepath.Join(dir, "configcrates", "alerts.vgo"), `
name: alerts
resources:
  - name: alerts-yaml
    type: file
    state: present
    target_path: /etc/prometheus/rules/vigo-alerts.yml
    content: |
      description: "`+literal+`"
      token: "secret:vigo/alertmanager/token"
`)
	writeFile(t, filepath.Join(dir, "default", "t.vgo"), `
envoys:
  - match: "alerts-host"
    configcrates: [alerts]
`)

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{
		"vigo/alertmanager/token": "tok-123",
	}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	_, cfg := store.Match("alerts-host")
	if cfg == nil {
		t.Fatal("Match(alerts-host) returned nil config")
	}
	if len(cfg.Configcrates) == 0 || len(cfg.Configcrates[0].Resources) == 0 {
		t.Fatalf("expected one resource on the alerts module, got %#v", cfg.Configcrates)
	}
	content, _ := cfg.Configcrates[0].Resources[0].Attributes["content"].(string)

	if !strings.Contains(content, literal) {
		t.Errorf("literal `secret:` text was not preserved verbatim; content = %q", content)
	}
	if !strings.Contains(content, "__VIGO_SECRET__vigo/alertmanager/token__END_VIGO_SECRET__") {
		t.Errorf("genuine embedded secret ref was not substituted; content = %q", content)
	}
	if strings.Contains(content, "secret:vigo/alertmanager/token") {
		t.Errorf("genuine embedded secret ref left unresolved; content = %q", content)
	}
	if strings.Contains(content, "tok-123") {
		t.Errorf("content leaks plaintext secret value: %q", content)
	}
}

// TestTraceInverse covers the four carrier shapes the inverse trace surfaces:
// (a) modules referenced directly on the envoy entry, (b) modules inherited
// via common.vgo, (c) modules pulled in via a role, and (d) usercrates
// referenced via the entry — plus the --include-tree path where a pattern
// matches no enrolled envoy.
func TestTraceInverse(t *testing.T) {
	dir := setupDir(t)

	// Configcrates.
	for _, name := range []string{"nginx", "tlsbase", "chrony"} {
		writeFile(t, filepath.Join(dir, "configcrates", name+".vgo"), fmt.Sprintf(`
name: %s
resources:
  - name: install-%s
    type: package
    package: %s
    state: present
`, name, name, name))
	}

	// Usercrate.
	writeFile(t, filepath.Join(dir, "usercrates", "alex.vgo"), `
name: alex
resources:
  - name: alex
    type: user
    state: present
`)

	// Role pulls in chrony.
	writeRoles(t, dir, `
- name: webserver
  configcrates: [chrony]
`)

	// Root common.vgo contributes tlsbase to every entry.
	writeFile(t, filepath.Join(dir, "common.vgo"), `
configcrates: [tlsbase]
`)

	// Two leaf entries:
	//   - web01 (real envoy via TraceInverse below) references nginx + role + usercrate
	//   - staging-only pattern that no envoy matches in the test
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web01.example.com"
    configcrates: [nginx]
    roles: [webserver]
    usercrates: [alex]
  - match: "staging-only.example.com"
    configcrates: [nginx]
`)

	cs, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// Only web01 is "enrolled" in the test — staging-only.example.com is
	// the unmatched-pattern case.
	envoys := []InverseEnvoyRef{
		{ID: "envoy-web01", Hostname: "web01.example.com"},
	}

	cases := []struct {
		name            string
		subject         string
		value           string
		includeTree     bool
		wantHosts       []string // expected hostnames in Envoys
		wantCarrier     string   // expected substring in Envoys[0].Carrier
		wantTreeMatches []string // expected Match values in TreeRefs
	}{
		{
			name:        "module direct on entry",
			subject:     "configcrate",
			value:       "nginx",
			wantHosts:   []string{"web01.example.com"},
			wantCarrier: "envoys.vgo",
		},
		{
			name:        "module inherited via common.vgo",
			subject:     "configcrate",
			value:       "tlsbase",
			wantHosts:   []string{"web01.example.com"},
			wantCarrier: "common.vgo",
		},
		{
			name:        "module pulled in by role",
			subject:     "configcrate",
			value:       "chrony",
			wantHosts:   []string{"web01.example.com"},
			wantCarrier: "role:webserver",
		},
		{
			name:        "usercrate on entry",
			subject:     "usercrate",
			value:       "alex",
			wantHosts:   []string{"web01.example.com"},
			wantCarrier: "envoys.vgo",
		},
		{
			name:            "include-tree surfaces unmatched pattern",
			subject:         "configcrate",
			value:           "nginx",
			includeTree:     true,
			wantHosts:       []string{"web01.example.com"},
			wantCarrier:     "envoys.vgo",
			wantTreeMatches: []string{"staging-only.example.com"},
		},
		{
			name:    "module not present anywhere returns empty",
			subject: "configcrate",
			value:   "nonexistent",
		},
		{
			name:    "invalid subject returns empty",
			subject: "unknown",
			value:   "nginx",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := cs.TraceInverse(tc.subject, tc.value, tc.includeTree, envoys)

			if got, want := len(r.Envoys), len(tc.wantHosts); got != want {
				t.Fatalf("len(Envoys) = %d, want %d (entries=%+v)", got, want, r.Envoys)
			}
			for i, h := range tc.wantHosts {
				if r.Envoys[i].Hostname != h {
					t.Errorf("Envoys[%d].Hostname = %q, want %q", i, r.Envoys[i].Hostname, h)
				}
			}
			if tc.wantCarrier != "" && len(r.Envoys) > 0 {
				if !strings.Contains(r.Envoys[0].Carrier, tc.wantCarrier) {
					t.Errorf("Envoys[0].Carrier = %q, want substring %q", r.Envoys[0].Carrier, tc.wantCarrier)
				}
			}
			if got, want := len(r.TreeRefs), len(tc.wantTreeMatches); got != want {
				t.Fatalf("len(TreeRefs) = %d, want %d (refs=%+v)", got, want, r.TreeRefs)
			}
			for i, m := range tc.wantTreeMatches {
				if r.TreeRefs[i].Match != m {
					t.Errorf("TreeRefs[%d].Match = %q, want %q", i, r.TreeRefs[i].Match, m)
				}
			}
		})
	}
}
