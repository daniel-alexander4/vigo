// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/secrets"
)

// newConfigTraceTestStore creates a ConfigStore with a basic config layout
// for config trace, tree, and search tests.
func newConfigTraceTestStore(t *testing.T) *config.ConfigStore {
	t.Helper()
	dir := t.TempDir()

	// Create directory structure.
	for _, sub := range []string{"stockpile/modules", "scaffolding"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Write envoys.vgo with envoy entries (not common.vgo, which is for inheritance).
	nodesYAML := `envoys:
  - match: "web01.prod"
    modules:
      - nginx
    environment: production
  - match: "app01.staging"
    modules:
      - nginx
    environment: staging
`
	if err := os.WriteFile(filepath.Join(dir, "scaffolding", "envoys.vgo"), []byte(nodesYAML), 0644); err != nil {
		t.Fatalf("write envoys.vgo: %v", err)
	}

	// Write a module.
	moduleYAML := `name: nginx

resources:
  - name: nginx-pkg
    type: package
    package: nginx
`
	if err := os.WriteFile(filepath.Join(dir, "stockpile", "modules", "nginx.vgo"), []byte(moduleYAML), 0644); err != nil {
		t.Fatalf("write nginx.vgo: %v", err)
	}

	provider, err := secrets.NewLocalProvider("", t.TempDir())
	if err != nil {
		t.Fatalf("new local provider: %v", err)
	}
	t.Cleanup(func() { provider.Close() })

	store, err := config.NewConfigStore(dir, provider, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	return store
}

func TestConfigTrace_Tree(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/tree", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var tree config.TreeNode
	if err := json.NewDecoder(rec.Body).Decode(&tree); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The root should have some structure.
	if tree.Dir != "" && tree.Dir != "." {
		t.Logf("tree root dir: %q", tree.Dir)
	}
}

func TestConfigTrace_SearchByMatch(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/search?match=web01", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result config.SearchResult
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result.Entries) == 0 {
		t.Error("expected at least one search result for 'web01'")
	}
}

func TestConfigTrace_SearchByModule(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/search?module=nginx", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result config.SearchResult
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result.Entries) == 0 {
		t.Error("expected at least one search result for module 'nginx'")
	}
}

func TestConfigTrace_SearchRequiresParameter(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/search", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty search, got %d", rec.Code)
	}
}

func TestConfigTrace_Trace(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/trace?hostname=web01.prod", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var trace config.ConfigTrace
	json.NewDecoder(rec.Body).Decode(&trace)

	if !trace.Matched {
		t.Error("expected web01.prod to match")
	}
	if trace.Hostname != "web01.prod" {
		t.Errorf("expected hostname web01.prod, got %s", trace.Hostname)
	}
}

func TestConfigTrace_TraceNoMatch(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/trace?hostname=unknown.host", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var trace config.ConfigTrace
	json.NewDecoder(rec.Body).Decode(&trace)

	if trace.Matched {
		t.Error("expected unknown.host not to match")
	}
}

func TestConfigTrace_TraceMissingHostname(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/trace", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing hostname, got %d", rec.Code)
	}
}

