// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
)

// testAssistant builds an assistant whose provider is never called: the
// requests these tests send are refused or rejected before any AI call.
func testAssistant() *ai.Assistant {
	return ai.NewAssistant(ai.NewOllamaProvider("http://127.0.0.1:1"), nil, nil, nil, nil, 0, 0, config.AISection{}, "", nil)
}

func roleRequest(method, target, role string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(""))
	return req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{
		ID:          "test-" + role,
		Username:    role,
		Role:        role,
		Permissions: auth.DefaultPermissions(role),
	}))
}

// TestUIRoutes_FleetReadGate: the compliance role has no fleet.read, so the
// fleet-data pages, fragments and the AI drawer must 403 when reached by
// URL through the registered mux; a viewer (fleet.read) passes the gate.
func TestUIRoutes_FleetReadGate(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.assistant = testAssistant() // registers POST /ui/ask
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	envoyID := h.dataSource.(*mockDataSource).data.envoys[0].id.String()
	cases := []struct {
		method, path string
		viewerWant   int
	}{
		{http.MethodGet, "/envoys/" + envoyID, http.StatusOK},
		{http.MethodGet, "/config/web-01.example.com", http.StatusOK},
		{http.MethodGet, "/envoys", http.StatusOK},
		{http.MethodGet, "/ui/fragments/envoy-config/" + envoyID, http.StatusOK},
		// Empty body: a viewer gets past the gate to the handler's own 400.
		{http.MethodPost, "/ui/ask", http.StatusBadRequest},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, roleRequest(c.method, c.path, "compliance"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("compliance %s %s = %d, want 403", c.method, c.path, rec.Code)
		}

		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, roleRequest(c.method, c.path, "viewer"))
		if rec.Code != c.viewerWant {
			t.Errorf("viewer %s %s = %d, want %d", c.method, c.path, rec.Code, c.viewerWant)
		}
	}
}

// TestUIRoutes_ComplianceReadGate: compliance surfaces stay open to the
// compliance role and refuse a user holding neither compliance.read nor
// any role default that grants it.
func TestUIRoutes_ComplianceReadGate(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	path := "/compliance"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, roleRequest(http.MethodGet, path, "compliance"))
	if rec.Code == http.StatusForbidden {
		t.Errorf("compliance role refused on %s", path)
	}

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{ID: "u", Username: "u", Role: "viewer", Permissions: []string{auth.PermFleetRead}}))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("user without compliance.read on %s = %d, want 403", path, rec.Code)
	}
}

// TestSetCommon_AIDrawerHiddenWithoutFleetRead keeps the drawer in step
// with the POST /ui/ask gate.
func TestSetCommon_AIDrawerHiddenWithoutFleetRead(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.assistant = testAssistant()
	for role, want := range map[string]bool{"viewer": true, "compliance": false} {
		var pd PageData
		h.setCommonWithRequest(&pd, roleRequest(http.MethodGet, "/", role))
		if pd.AIEnabled != want {
			t.Errorf("%s: AIEnabled = %v, want %v", role, pd.AIEnabled, want)
		}
	}
}
