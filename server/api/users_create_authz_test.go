// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// TestUsers_CreateAuthzBeforeBody: a non-admin POST to /api/v1/users is
// refused before its body is read, so a malformed body cannot be used to
// probe validation (it used to answer 400 before checking the caller).
func TestUsers_CreateAuthzBeforeBody(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	mux := http.NewServeMux()
	NewUsersHandler(vigoauth.NewUserStore(queries)).RegisterRoutes(mux)

	for _, body := range []string{`not json`, `{"username":""}`, `{"username":"x","role":"bogus"}`} {
		req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "viewer-id", "viewer", "viewer")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("viewer, body %q: want 403, got %d", body, rec.Code)
		}
	}
}
