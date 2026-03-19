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
	"time"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/secrets"
)

// newPreviewTestDeps creates a ConfigStore and FleetIndex for preview tests.
func newPreviewTestDeps(t *testing.T) (*config.ConfigStore, *fleet.FleetIndex) {
	t.Helper()
	dir := t.TempDir()

	for _, sub := range []string{"modules"} {
		os.MkdirAll(filepath.Join(dir, sub), 0755)
	}

	// Write nodes.vgo with entries (not common.vgo, which is for inheritance).
	nodesYAML := `envoys:
  - match: "web01.prod"
    modules:
      - nginx
  - match: "web02.prod"
    modules:
      - nginx
  - match: "db01.prod"
    modules:
      - postgres
`
	os.WriteFile(filepath.Join(dir, "nodes.vgo"), []byte(nodesYAML), 0644)

	moduleYAML := `name: nginx

resources:
  - name: nginx-pkg
    type: package
    package: nginx
`
	os.WriteFile(filepath.Join(dir, "modules", "nginx.vgo"), []byte(moduleYAML), 0644)

	pgYAML := `name: postgres

resources:
  - name: pg-pkg
    type: package
    package: postgresql
`
	os.WriteFile(filepath.Join(dir, "modules", "postgres.vgo"), []byte(pgYAML), 0644)

	provider, err := secrets.NewLocalProvider("", t.TempDir())
	if err != nil {
		t.Fatalf("new local provider: %v", err)
	}
	t.Cleanup(func() { provider.Close() })

	store, err := config.NewConfigStore(dir, provider, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// Add envoys to the FleetIndex.
	fi := fleet.NewFleetIndex()
	for _, h := range []struct{ id, hostname string }{
		{"t1", "web01.prod"},
		{"t2", "web02.prod"},
		{"t3", "db01.prod"},
	} {
		fi.AddEnvoy(fleet.EnvoyState{
			ID: h.id, Hostname: h.hostname, IP: "10.0.0.1",
			EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
		}, nil)
	}

	return store, fi
}

func TestPreview_AffectedEnvoys(t *testing.T) {
	store, fi := newPreviewTestDeps(t)

	handler := NewPreviewHandler(store, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/preview?module=nginx", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Module   string `json:"module"`
		Affected []struct {
			Hostname string `json:"hostname"`
			EnvoyID string `json:"envoy_id"`
		} `json:"affected"`
		Count int `json:"count"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Module != "nginx" {
		t.Errorf("expected module nginx, got %s", resp.Module)
	}
	if resp.Count != 2 {
		t.Errorf("expected 2 affected envoys for nginx, got %d", resp.Count)
	}
}

func TestPreview_NoAffectedEnvoys(t *testing.T) {
	store, fi := newPreviewTestDeps(t)

	handler := NewPreviewHandler(store, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/preview?module=nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Count int `json:"count"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Count != 0 {
		t.Errorf("expected 0 affected for nonexistent module, got %d", resp.Count)
	}
}

func TestPreview_MissingModuleParameter(t *testing.T) {
	store, fi := newPreviewTestDeps(t)

	handler := NewPreviewHandler(store, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/preview", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing module, got %d", rec.Code)
	}
}

func TestPreview_NilFleetIndex(t *testing.T) {
	store, _ := newPreviewTestDeps(t)

	handler := NewPreviewHandler(store, nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/preview?module=nginx", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501 without FleetIndex, got %d", rec.Code)
	}
}
