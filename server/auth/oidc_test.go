// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/testutil"
)

// setupOIDCProvider returns an OIDCProvider wired to a real SCS session
// store + a seeded admin. oauth2Cfg / verifier are left nil — the
// Middleware path under test doesn't reach them (only /login and
// /auth/callback do).
func setupOIDCProvider(t *testing.T) (*OIDCProvider, *SessionManager, *User) {
	t.Helper()
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := db.New(sqlDB)

	sm := NewSessionManager(sqlDB, 0)
	userStore := NewUserStore(queries)
	tokenStore := NewTokenStore(queries, testLocalAdminToken, metrics.NewTestMetrics())

	alice, err := userStore.Create(context.Background(), db.InsertUserParams{
		Username: "alice",
		Role:     "admin",
	})
	require.NoError(t, err)

	provider := &OIDCProvider{
		sessions:   sm,
		userStore:  userStore,
		tokenStore: tokenStore,
	}
	return provider, sm, alice
}

func runOIDCMiddleware(provider *OIDCProvider, req *http.Request) (*User, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	var sawUser *User
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	provider.Middleware(downstream).ServeHTTP(rec, req)
	return sawUser, rec
}

// TestOIDCMiddleware_NoAuth_LocalAPI_Returns401 is the peer-auth-hole
// regression under OIDC: a loopback request with no credential must not be
// granted admin — source IP is not proof of root.
func TestOIDCMiddleware_NoAuth_LocalAPI_Returns401(t *testing.T) {
	provider, _, _ := setupOIDCProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52100"

	got, rec := runOIDCMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "loopback alone must not grant admin under OIDC")
	assert.Nil(t, got)
}

// TestOIDCMiddleware_LocalAdminToken_GrantsAdmin confirms the local-admin
// token resolves to the server-host admin under OIDC — the credential that
// replaced source-IP peer auth works regardless of the configured method.
func TestOIDCMiddleware_LocalAdminToken_GrantsAdmin(t *testing.T) {
	provider, _, _ := setupOIDCProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52101"
	req.Header.Set("Authorization", "Bearer "+testLocalAdminToken)

	got, rec := runOIDCMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "localhost", got.Username)
	assert.Equal(t, "admin", got.Role)
}

// TestOIDCMiddleware_Session_LocalAPI confirms a session cookie resolves to
// the real session user under OIDC.
func TestOIDCMiddleware_Session_LocalAPI(t *testing.T) {
	provider, sm, alice := setupOIDCProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52102"
	req.AddCookie(newSessionCookie(t, sm, alice))

	got, rec := runOIDCMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username, "an API request with a session cookie under OIDC must resolve to the session user")
}

// TestOIDCMiddleware_NoAuth_RemoteAPI_Returns401 confirms a remote request
// without a session or token gets 401.
func TestOIDCMiddleware_NoAuth_RemoteAPI_Returns401(t *testing.T) {
	provider, _, _ := setupOIDCProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "203.0.113.5:40000" // TEST-NET-3, non-local

	got, rec := runOIDCMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Nil(t, got)
}
