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
	tokenStore := NewTokenStore(queries)

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

// TestOIDCMiddleware_NoAuth_LocalAPI_FallsToPeerAuth confirms the bug
// described in pending.md: on OIDC-configured servers, localhost vigocli
// must still work via peer-auth fallback instead of failing with 401.
func TestOIDCMiddleware_NoAuth_LocalAPI_FallsToPeerAuth(t *testing.T) {
	provider, _, _ := setupOIDCProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52100"

	got, rec := runOIDCMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "localhost", got.Username, "vigocli on OIDC server must hit peer-auth fallback")
	assert.Equal(t, "admin", got.Role)
}

// TestOIDCMiddleware_SessionWinsOverPeerAuth_LocalAPI pins the ordering
// invariant: a browser on the server host with a valid session cookie
// must be attributed to the session user, not the synthetic localhost
// admin. Same regression as basic.go's Scrier 519 bug.
func TestOIDCMiddleware_SessionWinsOverPeerAuth_LocalAPI(t *testing.T) {
	provider, sm, alice := setupOIDCProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52101"
	req.AddCookie(newSessionCookie(t, sm, alice))

	got, rec := runOIDCMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username, "local API request with session cookie under OIDC must resolve to the session user, not the loopback admin")
}

// TestOIDCMiddleware_NoAuth_RemoteAPI_Returns401 confirms peer-auth is
// local-only under OIDC — a remote request without a session or token
// still gets 401, no accidental admin grant.
func TestOIDCMiddleware_NoAuth_RemoteAPI_Returns401(t *testing.T) {
	provider, _, _ := setupOIDCProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "203.0.113.5:40000" // TEST-NET-3, non-local

	got, rec := runOIDCMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Nil(t, got)
}
