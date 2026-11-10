// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAPI_ServeSpec(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.json")
	specContent := `{"openapi":"3.0.0","info":{"title":"Vigo","version":"1.0"}}`
	if err := os.WriteFile(specPath, []byte(specContent), 0644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	handler := NewOpenAPIHandler(specPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/openapi.json", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", rec.Header().Get("Content-Type"))
	}

	if rec.Body.String() != specContent {
		t.Errorf("expected spec content, got %s", rec.Body.String())
	}
}

func TestOpenAPI_SpecNotFound(t *testing.T) {
	handler := NewOpenAPIHandler("/nonexistent/openapi.json")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/openapi.json", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
