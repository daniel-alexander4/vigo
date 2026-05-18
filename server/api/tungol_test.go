// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/tungol"
)

func TestTungolStatusStandalone(t *testing.T) {
	handler := NewTungolHandler("", "", "", "", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/tungol/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp TungolStatus
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Role != "standalone" {
		t.Errorf("Role = %q, want standalone", resp.Role)
	}
	if resp.SteorraID != "" {
		t.Errorf("SteorraID = %q, want empty", resp.SteorraID)
	}
	if resp.Steorras != nil {
		t.Errorf("Steorras = %v, want nil", resp.Steorras)
	}
}

func TestTungolStatusWithSteorraRole(t *testing.T) {
	handler := NewTungolHandler(config.RoleSpoke, "test-tungol", "steorra-east-1", "", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/tungol/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp TungolStatus
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Role != "spoke" {
		t.Errorf("Role = %q, want spoke", resp.Role)
	}
	if resp.SteorraID != "steorra-east-1" {
		t.Errorf("SteorraID = %q, want steorra-east-1", resp.SteorraID)
	}
}

func TestTungolStatusHubNilSteorras(t *testing.T) {
	// Hub role but nil hub -- no steorras connected. Status still returns role.
	handler := NewTungolHandler(config.RoleHub, "test-tungol", "", "", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/tungol/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp TungolStatus
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Role != "hub" {
		t.Errorf("Role = %q, want hub", resp.Role)
	}
	if resp.Steorras != nil {
		t.Errorf("Steorras = %v, want nil (no hub)", resp.Steorras)
	}
}

func TestTungolStatusResponseFormat(t *testing.T) {
	handler := NewTungolHandler("", "", "", "", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/tungol/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestSteorraDrilldownRoutesNotRegisteredWithoutHub(t *testing.T) {
	handler := NewTungolHandler(config.RoleStandalone, "", "", "", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/tungol/steorras/steorra-1"},
		{"GET", "/api/v1/tungol/steorras/steorra-1/envoys"},
		{"GET", "/api/v1/tungol/steorras/steorra-1/runs"},
		{"POST", "/api/v1/tungol/steorras/steorra-1/push"},
		{"POST", "/api/v1/tungol/reassign"},
		{"POST", "/api/v1/tungol/steorras/steorra-1/drain"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, adminReq(req))

			if rec.Code == http.StatusOK {
				t.Errorf("expected non-200 for %s %s without hub, got %d", ep.method, ep.path, rec.Code)
			}
		})
	}
}

func TestTungolStatusJSON(t *testing.T) {
	handler := NewTungolHandler(config.RoleSpoke, "test-tungol", "east-1", "", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/tungol/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var raw map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, ok := raw["role"]; !ok {
		t.Error("missing 'role' key in response JSON")
	}
	if _, ok := raw["steorra_id"]; !ok {
		t.Error("missing 'steorra_id' key in response JSON")
	}
}

var _ = tungol.SteorraStatus{} // ensure type is importable
