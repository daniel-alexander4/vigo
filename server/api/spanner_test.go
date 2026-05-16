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

func TestSpannerStatusStandalone(t *testing.T) {
	handler := NewSpannerHandler("", "", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/spanner/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp SpannerStatus
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Role != "standalone" {
		t.Errorf("Role = %q, want standalone", resp.Role)
	}
	if resp.SpokeID != "" {
		t.Errorf("SpokeID = %q, want empty", resp.SpokeID)
	}
	if resp.Spokes != nil {
		t.Errorf("Spokes = %v, want nil", resp.Spokes)
	}
}

func TestSpannerStatusWithSpokeRole(t *testing.T) {
	handler := NewSpannerHandler(config.RoleSpoke, "spoke-east-1", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/spanner/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp SpannerStatus
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Role != "spoke" {
		t.Errorf("Role = %q, want spoke", resp.Role)
	}
	if resp.SpokeID != "spoke-east-1" {
		t.Errorf("SpokeID = %q, want spoke-east-1", resp.SpokeID)
	}
}

func TestSpannerStatusHubNilSpokes(t *testing.T) {
	// Hub role but nil hub -- no spokes connected. Status still returns role.
	handler := NewSpannerHandler(config.RoleHub, "", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/spanner/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp SpannerStatus
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Role != "hub" {
		t.Errorf("Role = %q, want hub", resp.Role)
	}
	// When hub is nil, spokes list should be nil/empty.
	if resp.Spokes != nil {
		t.Errorf("Spokes = %v, want nil (no hub)", resp.Spokes)
	}
}

func TestSpannerStatusResponseFormat(t *testing.T) {
	handler := NewSpannerHandler("", "", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/spanner/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestSpokeDrilldownRoutesNotRegisteredWithoutHub(t *testing.T) {
	// When hub is nil, spoke drill-down routes should not be registered.
	handler := NewSpannerHandler(config.RoleStandalone, "", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/spanner/spokes/spoke-1"},
		{"GET", "/api/v1/spanner/spokes/spoke-1/envoys"},
		{"GET", "/api/v1/spanner/spokes/spoke-1/runs"},
		{"POST", "/api/v1/spanner/spokes/spoke-1/push"},
		{"POST", "/api/v1/spanner/reassign"},
		{"POST", "/api/v1/spanner/spokes/spoke-1/drain"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, adminReq(req))

			// Without a hub, these routes are not registered, so the mux
			// returns 404 or 405.
			if rec.Code == http.StatusOK {
				t.Errorf("expected non-200 for %s %s without hub, got %d", ep.method, ep.path, rec.Code)
			}
		})
	}
}

func TestSpannerStatusJSON(t *testing.T) {
	// Verify the JSON structure has the expected keys.
	handler := NewSpannerHandler(config.RoleSpoke, "east-1", nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/spanner/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var raw map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, ok := raw["role"]; !ok {
		t.Error("missing 'role' key in response JSON")
	}
	if _, ok := raw["spoke_id"]; !ok {
		t.Error("missing 'spoke_id' key in response JSON")
	}
}

// Verify the SpannerStatus type matches the expected JSON structure.
var _ = tungol.SpokeStatus{} // ensure type is importable
