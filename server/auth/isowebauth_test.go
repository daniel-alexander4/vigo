// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/httpsec"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/testutil"
)

// setupIsowebauthProvider returns an IsowebauthProvider wired to a real
// SCS session store + a seeded admin.
func setupIsowebauthProvider(t *testing.T) (*IsowebauthProvider, *SessionManager, *User) {
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

	provider := &IsowebauthProvider{
		sessions:   sm,
		userStore:  userStore,
		tokenStore: tokenStore,
	}
	return provider, sm, alice
}

func runIsowebauthMiddleware(provider *IsowebauthProvider, req *http.Request) (*User, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	var sawUser *User
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	provider.Middleware(downstream).ServeHTTP(rec, req)
	return sawUser, rec
}

// TestIsowebauthMiddleware_NoAuth_LocalAPI_Returns401 is the peer-auth-hole
// regression under isowebauth: a loopback request with no credential must
// not be granted admin.
func TestIsowebauthMiddleware_NoAuth_LocalAPI_Returns401(t *testing.T) {
	provider, _, _ := setupIsowebauthProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52200"

	got, rec := runIsowebauthMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "loopback alone must not grant admin under isowebauth")
	assert.Nil(t, got)
}

// TestIsowebauthMiddleware_LocalAdminToken_GrantsAdmin confirms the
// local-admin token resolves to the server-host admin under isowebauth.
func TestIsowebauthMiddleware_LocalAdminToken_GrantsAdmin(t *testing.T) {
	provider, _, _ := setupIsowebauthProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52201"
	req.Header.Set("Authorization", "Bearer "+testLocalAdminToken)

	got, rec := runIsowebauthMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "localhost", got.Username)
	assert.Equal(t, "admin", got.Role)
}

// TestIsowebauthMiddleware_Session_LocalAPI confirms a session cookie
// resolves to the real session user under isowebauth.
func TestIsowebauthMiddleware_Session_LocalAPI(t *testing.T) {
	provider, sm, alice := setupIsowebauthProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52202"
	req.AddCookie(newSessionCookie(t, sm, alice))

	got, rec := runIsowebauthMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username, "an API request with a session cookie under isowebauth must resolve to the session user")
}

// TestIsowebauthMiddleware_NoAuth_RemoteAPI_Returns401 confirms a remote
// request without a session or token gets 401.
func TestIsowebauthMiddleware_NoAuth_RemoteAPI_Returns401(t *testing.T) {
	provider, _, _ := setupIsowebauthProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "203.0.113.5:40000"

	got, rec := runIsowebauthMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Nil(t, got)
}

// TestIsowebauthLoginPage_CSPAllowsLocalSigner: the login script fetches
// the local signing app; the locked CSP alone would block it, so the
// login page — and only it — carries the connect-src for that origin.
func TestIsowebauthLoginPage_CSPAllowsLocalSigner(t *testing.T) {
	provider := &IsowebauthProvider{}
	rec := httptest.NewRecorder()
	provider.handleLoginPage(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "connect-src 'self' "+httpsec.IsowebauthSignerOrigin)
	assert.Equal(t, httpsec.IsowebauthLoginCSP, csp)
	assert.NotContains(t, httpsec.CSP, "7890", "the locked CSP must not carry the isowebauth signer origin")
}
