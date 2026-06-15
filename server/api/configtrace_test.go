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

	if err := os.MkdirAll(filepath.Join(dir, "configcrates"), 0755); err != nil {
		t.Fatalf("mkdir modules: %v", err)
	}

	// Write envoys.vgo with envoy entries (not common.vgo, which is for inheritance).
	nodesYAML := `envoys:
  - match: "web01.prod"
    configcrates:
      - nginx
    environment: production
  - match: "app01.staging"
    configcrates:
      - nginx
    environment: staging
`
	if err := os.WriteFile(filepath.Join(dir, "envoys.vgo"), []byte(nodesYAML), 0644); err != nil {
		t.Fatalf("write envoys.vgo: %v", err)
	}

	// Write a module.
	configcrateYAML := `name: nginx

resources:
  - name: nginx-pkg
    type: package
    package: nginx
`
	if err := os.WriteFile(filepath.Join(dir, "configcrates", "nginx.vgo"), []byte(configcrateYAML), 0644); err != nil {
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
	mux.ServeHTTP(rec, adminReq(req))

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
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result config.SearchResult
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result.Entries) == 0 {
		t.Error("expected at least one search result for 'web01'")
	}
}

func TestConfigTrace_SearchByConfigcrate(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/search?configcrate=nginx", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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
	mux.ServeHTTP(rec, adminReq(req))

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
	mux.ServeHTTP(rec, adminReq(req))

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
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var trace config.ConfigTrace
	json.NewDecoder(rec.Body).Decode(&trace)

	if trace.Matched {
		t.Error("expected unknown.host not to match")
	}
}

func TestConfigTrace_TraceNoSubject(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// No hostname, no module, no usercrate — no subject at all.
	req := httptest.NewRequest("GET", "/api/v1/config/trace", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing subject, got %d", rec.Code)
	}
}

// TestConfigTrace_InverseConfigcrate exercises the inverse (subject-rooted) branch:
// ?configcrate= without a hostname returns an InverseTraceResult rather than a
// forward ConfigTrace.
func TestConfigTrace_InverseConfigcrate(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/trace?configcrate=nginx", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result config.InverseTraceResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Subject != "configcrate" || result.Value != "nginx" {
		t.Errorf("expected subject=configcrate value=nginx, got %s/%s", result.Subject, result.Value)
	}
}

// TestConfigTrace_InverseUsercrate confirms ?usercrate= selects the usercrate
// subject.
func TestConfigTrace_InverseUsercrate(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/trace?usercrate=alexander4", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result config.InverseTraceResult
	json.NewDecoder(rec.Body).Decode(&result)
	if result.Subject != "usercrate" || result.Value != "alexander4" {
		t.Errorf("expected subject=usercrate value=alexander4, got %s/%s", result.Subject, result.Value)
	}
}

// TestConfigTrace_InverseIncludeTree confirms include_tree widens the result
// to config-tree patterns. With no fleet index there are no enrolled envoys,
// so every envoyEntry that references nginx surfaces as an unmatched
// config-tree pattern.
func TestConfigTrace_InverseIncludeTree(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/trace?configcrate=nginx&include_tree=true", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result config.InverseTraceResult
	json.NewDecoder(rec.Body).Decode(&result)
	if !result.IncludeTree {
		t.Error("expected include_tree true")
	}
	// envoys.vgo has two entries (web01.prod, app01.staging), both pulling nginx.
	if len(result.TreeRefs) != 2 {
		t.Errorf("expected 2 config-tree refs, got %d", len(result.TreeRefs))
	}
}

// TestConfigTrace_InverseConfigcrateUsercrateConflict confirms ?configcrate= and
// ?usercrate= together are rejected — they each select a different subject.
func TestConfigTrace_InverseConfigcrateUsercrateConflict(t *testing.T) {
	store := newConfigTraceTestStore(t)

	handler := NewConfigTraceHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/trace?configcrate=nginx&usercrate=alexander4", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for module+usercrate conflict, got %d", rec.Code)
	}
}
