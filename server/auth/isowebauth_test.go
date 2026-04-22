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

// setupIsowebauthProvider returns an IsowebauthProvider wired to a real
// SCS session store + a seeded admin.
func setupIsowebauthProvider(t *testing.T) (*IsowebauthProvider, *SessionManager, *User) {
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

// TestIsowebauthMiddleware_NoAuth_LocalAPI_FallsToPeerAuth confirms the
// bug described in pending.md: on isowebauth-configured servers,
// localhost vigocli must still work via peer-auth fallback.
func TestIsowebauthMiddleware_NoAuth_LocalAPI_FallsToPeerAuth(t *testing.T) {
	provider, _, _ := setupIsowebauthProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52200"

	got, rec := runIsowebauthMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "localhost", got.Username, "vigocli on isowebauth server must hit peer-auth fallback")
	assert.Equal(t, "admin", got.Role)
}

// TestIsowebauthMiddleware_SessionWinsOverPeerAuth_LocalAPI pins the
// ordering invariant under isowebauth.
func TestIsowebauthMiddleware_SessionWinsOverPeerAuth_LocalAPI(t *testing.T) {
	provider, sm, alice := setupIsowebauthProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52201"
	req.AddCookie(newSessionCookie(t, sm, alice))

	got, rec := runIsowebauthMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username, "local API request with session cookie under isowebauth must resolve to the session user, not the loopback admin")
}

// TestIsowebauthMiddleware_NoAuth_RemoteAPI_Returns401 confirms
// peer-auth is local-only under isowebauth.
func TestIsowebauthMiddleware_NoAuth_RemoteAPI_Returns401(t *testing.T) {
	provider, _, _ := setupIsowebauthProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "203.0.113.5:40000"

	got, rec := runIsowebauthMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Nil(t, got)
}
