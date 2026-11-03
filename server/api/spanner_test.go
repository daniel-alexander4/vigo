// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.alexander4.io/vigo/server/config"
)

func TestSpannerStatusStandalone(t *testing.T) {
	handler := NewSpannerHandler("", "", "")
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

	if resp.Mode != "standalone" {
		t.Errorf("Mode = %q, want standalone", resp.Mode)
	}
	if resp.PeerObservability != nil {
		t.Errorf("PeerObservability = %v, want nil", resp.PeerObservability)
	}
}

func TestSpannerStatusSpanner(t *testing.T) {
	handler := NewSpannerHandler(config.ModeSpanner, "test-spanner", "abcdef0123456789")
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

	if resp.Mode != "spanner" {
		t.Errorf("Mode = %q, want spanner", resp.Mode)
	}
	if resp.SpannerID != "test-spanner" {
		t.Errorf("SpannerID = %q, want test-spanner", resp.SpannerID)
	}
	if resp.LocalBoltPubkey != "abcdef0123456789" {
		t.Errorf("LocalBoltPubkey = %q, want abcdef0123456789", resp.LocalBoltPubkey)
	}
}

func TestSpannerStatusResponseFormat(t *testing.T) {
	handler := NewSpannerHandler("", "", "")
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

// In standalone mode the bolt drill-down routes are registered but
// have no spanner admin behind them, so every one returns non-200.
func TestBoltDrilldownRoutesInertWithoutSpannerAdmin(t *testing.T) {
	handler := NewSpannerHandler(config.ModeStandalone, "", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/spanner/bolts/bolt-1"},
		{"GET", "/api/v1/spanner/bolts/bolt-1/envoys"},
		{"GET", "/api/v1/spanner/bolts/bolt-1/runs"},
		{"POST", "/api/v1/spanner/bolts/bolt-1/push"},
		{"POST", "/api/v1/spanner/reassign"},
		{"POST", "/api/v1/spanner/bolts/bolt-1/drain"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, adminReq(req))

			if rec.Code == http.StatusOK {
				t.Errorf("expected non-200 for %s %s without spanner admin, got %d", ep.method, ep.path, rec.Code)
			}
		})
	}
}

func TestSpannerStatusJSON(t *testing.T) {
	handler := NewSpannerHandler(config.ModeSpanner, "test-spanner", "pubkey-x")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/spanner/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var raw map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, ok := raw["mode"]; !ok {
		t.Error("missing 'mode' key in response JSON")
	}
	if _, ok := raw["spanner_id"]; !ok {
		t.Error("missing 'spanner_id' key in response JSON")
	}
}
