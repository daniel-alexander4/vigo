// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
)

// insertTestUser inserts a user and returns their ID.
func insertTestUser(t *testing.T, sqlDB *sql.DB, username, role string) string {
	t.Helper()
	var id string
	err := sqlDB.QueryRow(
		`INSERT INTO users (username, role) VALUES (?, ?) RETURNING id`,
		username, role,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

// tokenRequestWithUser creates an HTTP request with an authenticated user in context.
func tokenRequestWithUser(method, path string, body []byte, userID, username, role string) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	ctx := vigoauth.ContextWithUser(req.Context(), &vigoauth.User{
		ID:       userID,
		Username: username,
		Role:     role,
	})
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestTokens_CreateToken(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	userID := insertTestUser(t, sqlDB, "alice", "admin")

	tokenStore := vigoauth.NewTokenStore(queries, "", metrics.NewTestMetrics())
	handler := NewTokensHandler(tokenStore, vigoauth.NewUserStore(queries))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"name":"my-ci-token","expires_in":"720h"}`
	req := tokenRequestWithUser("POST", "/api/v1/auth/tokens", []byte(body), userID, "alice", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp createTokenResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token == "" {
		t.Error("expected token to be returned")
	}
	if resp.Name != "my-ci-token" {
		t.Errorf("expected name my-ci-token, got %s", resp.Name)
	}
	if resp.ID == "" {
		t.Error("expected id to be returned")
	}
}

// The local-admin principal (root on the server host) has no users-table
// row, so it must name a real owner via --user; without it the token would
// be orphaned and could never authenticate. A real web user may not set
// --user at all — this endpoint is auth-gated, not admin-gated, so allowing
// it would let a viewer mint an admin-owned token.
func TestTokens_CreateTokenOwner(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	aliceID := insertTestUser(t, sqlDB, "alice", "admin")
	insertTestUser(t, sqlDB, "bob", "viewer")

	tokenStore := vigoauth.NewTokenStore(queries, "", metrics.NewTestMetrics())
	handler := NewTokensHandler(tokenStore, vigoauth.NewUserStore(queries))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	post := func(body, userID, username, role string) *httptest.ResponseRecorder {
		req := tokenRequestWithUser("POST", "/api/v1/auth/tokens", []byte(body), userID, username, role)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Local-admin without --user: orphan owner rejected.
	if rec := post(`{"name":"t"}`, vigoauth.LocalAdminUserID, "localhost", "admin"); rec.Code != http.StatusBadRequest {
		t.Fatalf("local-admin no --user: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Local-admin with a non-existent --user: rejected.
	if rec := post(`{"name":"t","user":"nobody"}`, vigoauth.LocalAdminUserID, "localhost", "admin"); rec.Code != http.StatusBadRequest {
		t.Fatalf("local-admin bad --user: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Local-admin with a valid --user: token minted and owned by that user.
	rec := post(`{"name":"t","user":"alice"}`, vigoauth.LocalAdminUserID, "localhost", "admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("local-admin valid --user: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp createTokenResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	var ownerID string
	if err := sqlDB.QueryRow(`SELECT user_id FROM api_tokens WHERE id = ?`, resp.ID).Scan(&ownerID); err != nil {
		t.Fatalf("read minted token owner: %v", err)
	}
	if ownerID != aliceID {
		t.Errorf("expected token owned by alice (%s), got %s", aliceID, ownerID)
	}

	// Real web user setting --user for someone else: forbidden (no escalation).
	if rec := post(`{"name":"t","user":"alice"}`, insertTestUser(t, sqlDB, "carol", "viewer"), "carol", "viewer"); rec.Code != http.StatusForbidden {
		t.Fatalf("real user with --user: expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTokens_CreateTokenInvalidExpiry(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	userID := insertTestUser(t, sqlDB, "alice", "admin")

	tokenStore := vigoauth.NewTokenStore(queries, "", metrics.NewTestMetrics())
	handler := NewTokensHandler(tokenStore, vigoauth.NewUserStore(queries))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"name":"test","expires_in":"not-a-duration"}`
	req := tokenRequestWithUser("POST", "/api/v1/auth/tokens", []byte(body), userID, "alice", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTokens_ListTokens(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	userID := insertTestUser(t, sqlDB, "alice", "admin")

	tokenStore := vigoauth.NewTokenStore(queries, "", metrics.NewTestMetrics())
	handler := NewTokensHandler(tokenStore, vigoauth.NewUserStore(queries))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Create two tokens.
	for _, name := range []string{"token-1", "token-2"} {
		body := `{"name":"` + name + `"}`
		req := tokenRequestWithUser("POST", "/api/v1/auth/tokens", []byte(body), userID, "alice", "admin")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s: expected 201, got %d", name, rec.Code)
		}
	}

	// List tokens.
	req := tokenRequestWithUser("GET", "/api/v1/auth/tokens", nil, userID, "alice", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var tokens []db.ListAPITokensByUserRow
	json.NewDecoder(rec.Body).Decode(&tokens)
	if len(tokens) != 2 {
		t.Errorf("expected 2 tokens, got %d", len(tokens))
	}
}

func TestTokens_DeleteToken(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	userID := insertTestUser(t, sqlDB, "alice", "admin")

	tokenStore := vigoauth.NewTokenStore(queries, "", metrics.NewTestMetrics())
	handler := NewTokensHandler(tokenStore, vigoauth.NewUserStore(queries))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Create a token.
	body := `{"name":"delete-me"}`
	req := tokenRequestWithUser("POST", "/api/v1/auth/tokens", []byte(body), userID, "alice", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", rec.Code)
	}

	var createResp createTokenResponse
	json.NewDecoder(rec.Body).Decode(&createResp)

	// Delete it.
	req2 := tokenRequestWithUser("DELETE", "/api/v1/auth/tokens/"+createResp.ID, nil, userID, "alice", "admin")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Verify list is empty.
	req3 := tokenRequestWithUser("GET", "/api/v1/auth/tokens", nil, userID, "alice", "admin")
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)

	var tokens []db.ListAPITokensByUserRow
	json.NewDecoder(rec3.Body).Decode(&tokens)
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens after delete, got %d", len(tokens))
	}
}

// TestTokens_DeleteAuthz verifies the owner-or-admin delete boundary: a
// non-owner non-admin (viewer) is denied 403, but the owner and an admin can
// both delete. Guards against the IDOR where any authenticated user could
// revoke another user's (incl. an admin's) token by ID.
func TestTokens_DeleteAuthz(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	aliceID := insertTestUser(t, sqlDB, "alice", "admin")
	bobID := insertTestUser(t, sqlDB, "bob", "viewer")

	tokenStore := vigoauth.NewTokenStore(queries, "", metrics.NewTestMetrics())
	handler := NewTokensHandler(tokenStore, vigoauth.NewUserStore(queries))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// alice creates a token.
	req := tokenRequestWithUser("POST", "/api/v1/auth/tokens", []byte(`{"name":"alices"}`), aliceID, "alice", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", rec.Code)
	}
	var created createTokenResponse
	json.NewDecoder(rec.Body).Decode(&created)

	del := func(userID, username, role string) int {
		r := tokenRequestWithUser("DELETE", "/api/v1/auth/tokens/"+created.ID, nil, userID, username, role)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}

	// bob (viewer, not the owner) is denied.
	if code := del(bobID, "bob", "viewer"); code != http.StatusForbidden {
		t.Fatalf("non-owner viewer: expected 403, got %d", code)
	}
	// A nonexistent token id → 404.
	r := tokenRequestWithUser("DELETE", "/api/v1/auth/tokens/does-not-exist", nil, bobID, "bob", "viewer")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing token: expected 404, got %d", w.Code)
	}
	// An admin who is not the owner can delete (offboarding).
	if code := del(bobID, "bob", "admin"); code != http.StatusNoContent {
		t.Fatalf("admin: expected 204, got %d", code)
	}
}

func TestTokens_UnauthorizedWithoutUser(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))

	tokenStore := vigoauth.NewTokenStore(queries, "", metrics.NewTestMetrics())
	handler := NewTokensHandler(tokenStore, vigoauth.NewUserStore(queries))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Request without auth context — middleware rejects.
	req := httptest.NewRequest("GET", "/api/v1/auth/tokens", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
