// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	hobnobauth "github.com/hobnob/hobnob/server/auth"
	"github.com/hobnob/hobnob/server/db"
)

// openTokensTestDB creates an in-memory SQLite database with users and api_tokens tables.
func openTokensTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE users (
			id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
			username        TEXT NOT NULL UNIQUE,
			display_name    TEXT NOT NULL DEFAULT '',
			email           TEXT NOT NULL DEFAULT '',
			role            TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('admin', 'viewer')),
			ssh_public_key  TEXT NOT NULL DEFAULT '',
			oidc_subject    TEXT NOT NULL DEFAULT '',
			created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE api_tokens (
			id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
			user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
			name         TEXT NOT NULL DEFAULT '',
			token_hash   TEXT NOT NULL,
			created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
			last_used_at DATETIME,
			expires_at   DATETIME
		);
		CREATE INDEX idx_api_tokens_user_id ON api_tokens (user_id);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB, db.New(sqlDB)
}

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
	ctx := hobnobauth.ContextWithUser(req.Context(), &hobnobauth.User{
		ID:       userID,
		Username: username,
		Role:     role,
	})
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestTokens_CreateToken(t *testing.T) {
	sqlDB, queries := openTokensTestDB(t)
	userID := insertTestUser(t, sqlDB, "alice", "admin")

	tokenStore := hobnobauth.NewTokenStore(queries)
	handler := NewTokensHandler(tokenStore, "none")
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

func TestTokens_CreateTokenInvalidExpiry(t *testing.T) {
	sqlDB, queries := openTokensTestDB(t)
	userID := insertTestUser(t, sqlDB, "alice", "admin")

	tokenStore := hobnobauth.NewTokenStore(queries)
	handler := NewTokensHandler(tokenStore, "none")
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
	sqlDB, queries := openTokensTestDB(t)
	userID := insertTestUser(t, sqlDB, "alice", "admin")

	tokenStore := hobnobauth.NewTokenStore(queries)
	handler := NewTokensHandler(tokenStore, "none")
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
	sqlDB, queries := openTokensTestDB(t)
	userID := insertTestUser(t, sqlDB, "alice", "admin")

	tokenStore := hobnobauth.NewTokenStore(queries)
	handler := NewTokensHandler(tokenStore, "none")
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

func TestTokens_UnauthorizedWithoutUser(t *testing.T) {
	_, queries := openTokensTestDB(t)

	tokenStore := hobnobauth.NewTokenStore(queries)
	handler := NewTokensHandler(tokenStore, "none")
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
