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

	vigoauth "github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/db"
)

// openUsersTestDB creates an in-memory SQLite database with the users table.
func openUsersTestDB(t *testing.T) (*sql.DB, *db.Queries) {
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
			permissions		TEXT NOT NULL DEFAULT '',
			ssh_public_key  TEXT NOT NULL DEFAULT '',
			oidc_subject    TEXT NOT NULL DEFAULT '',
			totp_enabled    INTEGER NOT NULL DEFAULT 0,
			totp_recovery   TEXT NOT NULL DEFAULT '',
			created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB, db.New(sqlDB)
}

// userRequestWithUser creates an HTTP request with an authenticated user in context.
func userRequestWithUser(method, path string, body []byte, userID, username, role string) *http.Request {
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

func TestUsers_CreateUser(t *testing.T) {
	_, queries := openUsersTestDB(t)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"username":"bob","display_name":"Bob Smith","email":"bob@example.com","role":"viewer"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var user vigoauth.User
	json.NewDecoder(rec.Body).Decode(&user)
	if user.Username != "bob" {
		t.Errorf("expected username bob, got %s", user.Username)
	}
	if user.Role != "viewer" {
		t.Errorf("expected role viewer, got %s", user.Role)
	}
	if user.ID == "" {
		t.Error("expected user ID to be set")
	}
}

func TestUsers_CreateUserMissingUsername(t *testing.T) {
	_, queries := openUsersTestDB(t)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"role":"viewer"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestUsers_CreateUserInvalidRole(t *testing.T) {
	_, queries := openUsersTestDB(t)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"username":"test","role":"superadmin"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid role, got %d", rec.Code)
	}
}

func TestUsers_CreateUserDefaultsToViewer(t *testing.T) {
	_, queries := openUsersTestDB(t)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"username":"norole"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var user vigoauth.User
	json.NewDecoder(rec.Body).Decode(&user)
	if user.Role != "viewer" {
		t.Errorf("expected default role viewer, got %s", user.Role)
	}
}

func TestUsers_ListUsers(t *testing.T) {
	sqlDB, queries := openUsersTestDB(t)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Create two users directly.
	for _, u := range []string{"alice", "bob"} {
		_, err := sqlDB.Exec(`INSERT INTO users (username, role) VALUES (?, 'viewer')`, u)
		if err != nil {
			t.Fatalf("insert %s: %v", u, err)
		}
	}

	req := userRequestWithUser("GET", "/api/v1/users", nil, "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var users []*vigoauth.User
	json.NewDecoder(rec.Body).Decode(&users)
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}

func TestUsers_DeleteUser(t *testing.T) {
	sqlDB, queries := openUsersTestDB(t)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	var userID string
	err := sqlDB.QueryRow(`INSERT INTO users (username, role) VALUES ('bob', 'viewer') RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := userRequestWithUser("DELETE", "/api/v1/users/"+userID, nil, "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUsers_DeleteSelfBlocked(t *testing.T) {
	sqlDB, queries := openUsersTestDB(t)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	var userID string
	err := sqlDB.QueryRow(`INSERT INTO users (username, role) VALUES ('admin', 'admin') RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Try to delete yourself — should be rejected.
	req := userRequestWithUser("DELETE", "/api/v1/users/"+userID, nil, userID, "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for self-delete, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUsers_RBACViewerCannotListUsers(t *testing.T) {
	_, queries := openUsersTestDB(t)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Viewer should be blocked by RequireAdmin middleware.
	req := userRequestWithUser("GET", "/api/v1/users", nil, "viewer-id", "viewer", "viewer")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d", rec.Code)
	}
}

func TestUsers_RBACViewerCannotCreate(t *testing.T) {
	_, queries := openUsersTestDB(t)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"username":"newuser","role":"viewer"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "viewer-id", "viewer", "viewer")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer creating user, got %d", rec.Code)
	}
}
