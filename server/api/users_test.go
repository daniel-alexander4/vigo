// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

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
	queries := db.New(openMigratedTestDB(t))
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

// TestUsers_Update_AdminWithoutPubkey_Rejected pins the invariant guard: an
// admin webuser must carry an SSH pubkey. A pubkey-less admin row — the
// broken state a viewer→admin promotion left behind before the set-role fix
// — must not survive an update; the handler rejects it rather than silently
// storing it for browser scrier to fail on later.
func TestUsers_Update_AdminWithoutPubkey_Rejected(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	u, err := store.Create(context.Background(), db.InsertUserParams{
		Username: "dan",
		Role:     "admin",
	})
	if err != nil {
		t.Fatalf("seeding pubkey-less admin: %v", err)
	}

	body := `{"display_name":"Dan A"}`
	req := userRequestWithUser("PUT", "/api/v1/users/"+u.ID, []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an admin with no SSH pubkey, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "SSH public key") {
		t.Errorf("expected an SSH-pubkey-requirement error, got: %s", rec.Body.String())
	}
}

func TestUsers_CreateUserMissingUsername(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
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
	queries := db.New(openMigratedTestDB(t))
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
	queries := db.New(openMigratedTestDB(t))
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

// --- admin username validation (per-operator OS-mapped admin) ---

// seedLocalAgent inserts an envoy with the given hostname and a `users`
// trait whose human field is the given list. Returns the queries handle
// the test should pass to SetAdminValidation.
func seedLocalAgent(t *testing.T, hostname string, human []string) (*db.Queries, *sql.DB) {
	t.Helper()
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)

	envoy, err := queries.CreateEnvoy(context.Background(), db.CreateEnvoyParams{
		Hostname:  hostname,
		IpAddress: "127.0.0.1",
		PubkeyPem: "test-pubkey",
	})
	if err != nil {
		t.Fatalf("creating local agent envoy: %v", err)
	}

	traitsJSON := mustMarshal(t, map[string]any{
		"users": map[string]any{
			"human":      human,
			"os":         []string{"daemon", "sshd"},
			"thirdparty": []string{},
			"logged_in":  human,
		},
	})
	if _, err := queries.InsertEnvoyTraits(context.Background(), db.InsertEnvoyTraitsParams{
		EnvoyID: envoy.ID,
		Traits:  string(traitsJSON),
	}); err != nil {
		t.Fatalf("inserting local agent traits: %v", err)
	}
	return queries, sqlDB
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestUsers_AdminValidation_Success(t *testing.T) {
	queries, sqlDB := seedLocalAgent(t, "vigo.test", []string{"dan", "alice"})
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	handler.SetAdminValidation(queries, "vigo.test")
	_ = sqlDB
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// An admin webuser must carry an SSH pubkey — the CLI auto-seeds it
	// from the OS user's home; supply one here so the create satisfies the
	// invariant the handler enforces.
	body := `{"username":"dan","role":"admin","ssh_public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEQiISM8yaMfJhysMR7ZM80c0w4lnwe/lgyPyzeSCEKI test@vigo"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUsers_AdminValidation_UsernameNotHuman(t *testing.T) {
	queries, _ := seedLocalAgent(t, "vigo.test", []string{"dan"})
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	handler.SetAdminValidation(queries, "vigo.test")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"username":"ghost","role":"admin"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("not a human OS user")) {
		t.Errorf("expected error mentioning 'not a human OS user'; got %s", rec.Body.String())
	}
}

func TestUsers_AdminValidation_NoLocalAgent(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	handler.SetAdminValidation(queries, "vigo.test") // no envoy with this hostname
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"username":"dan","role":"admin"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("not yet enrolled")) {
		t.Errorf("expected 'not yet enrolled' error; got %s", rec.Body.String())
	}
}

func TestUsers_AdminValidation_NoTraitsYet(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	if _, err := queries.CreateEnvoy(context.Background(), db.CreateEnvoyParams{
		Hostname:  "vigo.test",
		IpAddress: "127.0.0.1",
		PubkeyPem: "x",
	}); err != nil {
		t.Fatalf("create envoy: %v", err)
	}
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	handler.SetAdminValidation(queries, "vigo.test")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"username":"dan","role":"admin"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("has not yet reported traits")) {
		t.Errorf("expected 'has not yet reported traits' error; got %s", rec.Body.String())
	}
}

func TestUsers_AdminValidation_NotConfigured(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	// SetAdminValidation NOT called.
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"username":"dan","role":"admin"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("not configured")) {
		t.Errorf("expected 'not configured' error; got %s", rec.Body.String())
	}
}

func TestUsers_AdminValidation_ViewerSkipsCheck(t *testing.T) {
	// Viewer / compliance users are one-offs — no OS-user constraint.
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	store := vigoauth.NewUserStore(queries)
	handler := NewUsersHandler(store, "none")
	handler.SetAdminValidation(queries, "vigo.test") // wired but no envoy/traits
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"username":"ghost-viewer","role":"viewer"}`
	req := userRequestWithUser("POST", "/api/v1/users", []byte(body), "admin-id", "admin", "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for viewer (no admin check), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUsers_ListUsers(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
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
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
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
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
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
	queries := db.New(openMigratedTestDB(t))
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
	queries := db.New(openMigratedTestDB(t))
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
