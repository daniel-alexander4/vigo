// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/daniel-alexander4/vigo/server/peer"
)

func TestPeerStatusNotConfigured(t *testing.T) {
	h := NewPeerHandler(nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/peer/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestPeerPromoteNotConfigured(t *testing.T) {
	h := NewPeerHandler(nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/peer/promote", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestPeerStatusConfigured(t *testing.T) {
	svc := peer.NewService("/srv/vigo", "peer", nil)
	svc.SetPrimaryAddr("primary.example.com:1530")
	svc.SetConfigVersion(42)

	h := NewPeerHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/peer/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var resp peerStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Role != "peer" {
		t.Errorf("role = %q, want %q", resp.Role, "peer")
	}
	if resp.PrimaryAddr != "primary.example.com:1530" {
		t.Errorf("primary_addr = %q, want %q", resp.PrimaryAddr, "primary.example.com:1530")
	}
	if resp.ConfigVersion != 42 {
		t.Errorf("config_version = %d, want 42", resp.ConfigVersion)
	}
}

func TestPeerPromoteConfigured(t *testing.T) {
	svc := peer.NewService("/srv/vigo", "peer", nil)

	h := NewPeerHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/peer/promote", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var resp peerPromoteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success {
		t.Errorf("success = false, message = %s", resp.Message)
	}

	// Verify role changed.
	if role := svc.Role(); role != "primary" {
		t.Errorf("role = %q, want %q after promote", role, "primary")
	}
}
