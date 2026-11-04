// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
)

// Fleet-wide per-user longdrawer/lockbox state exposes filename lists
// and peer sets, so every mesh endpoint must be admin-only. Covers the
// four /api/v1 routes registered in MeshHandler.RegisterRoutes.
func TestMesh_RBACViewerBlocked(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	handler := NewMeshHandler(fleet.NewFleetIndex(), queries)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	paths := []string{
		"/api/v1/swarm/longdrawer/fleet",
		"/api/v1/swarm/lockbox/fleet",
		"/api/v1/swarm/longdrawer/envoy/abc/users/alice/files",
		"/api/v1/swarm/lockbox/envoy/abc/users/alice/files",
	}
	for _, p := range paths {
		req := userRequestWithUser("GET", p, nil, "viewer-id", "viewer", "viewer")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("viewer %s: expected 403, got %d", p, rec.Code)
		}
	}
}

// Admin should pass the RBAC gate and reach the handler. We don't care
// about the response body here — only that 403 isn't the answer.
func TestMesh_RBACAdminAllowed(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	handler := NewMeshHandler(fleet.NewFleetIndex(), queries)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := userRequestWithUser("GET", "/api/v1/swarm/longdrawer/fleet", nil, "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("admin unexpectedly blocked by RBAC: %d", rec.Code)
	}
}
