// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	vigoauth "git.alexander4.io/vigo/server/auth"
)

// TestAskRoute_FleetReadGate: POST /api/v1/ask carries the same gate as the
// web-UI drawer (POST /ui/ask, ui/route_rbac_test.go). A viewer holds
// fleet.read and reaches the handler, whose own 400 for an empty body
// proves the gate passed; compliance has no fleet.read and gets 403.
func TestAskRoute_FleetReadGate(t *testing.T) {
	mux := http.NewServeMux()
	NewAIHandler(nil).RegisterRoutes(mux)

	for role, want := range map[string]int{
		"viewer":     http.StatusBadRequest,
		"compliance": http.StatusForbidden,
		"admin":      http.StatusBadRequest,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ask", strings.NewReader(""))
		req = req.WithContext(vigoauth.ContextWithUser(req.Context(), &vigoauth.User{
			ID:          "test-" + role,
			Username:    role,
			Role:        role,
			Permissions: vigoauth.DefaultPermissions(role),
		}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s POST /api/v1/ask = %d, want %d", role, rec.Code, want)
		}
	}
}
