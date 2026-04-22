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
	"git.alexander4.io/vigo/server/secrets"
	"git.alexander4.io/vigo/server/testutil"
)

// setupBasicProvider returns a fully-wired BasicProvider backed by a real
// SCS session store plus a seeded admin user. The real SessionManager lets
// tests round-trip session cookies exactly the way production does.
func setupBasicProvider(t *testing.T) (*BasicProvider, *SessionManager, *UserStore, *TokenStore, *User) {
	t.Helper()
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := db.New(sqlDB)

	sm := NewSessionManager(sqlDB, 0)
	userStore := NewUserStore(queries)
	tokenStore := NewTokenStore(queries)

	// Seed an admin user so needsSetup() returns false for unauthenticated
	// fall-through paths. Provider uses NullProvider which returns
	// ("", nil) — needsSetup treats a nil error as "password set" and
	// returns false.
	admin, err := userStore.Create(context.Background(), db.InsertUserParams{
		Username: "alice",
		Role:     "admin",
	})
	require.NoError(t, err)

	provider := NewBasicProvider(sm, userStore, tokenStore, secrets.NullProvider{})
	return provider, sm, userStore, tokenStore, admin
}

// newSessionCookie pre-populates a session with the given user and returns a
// cookie that references it. Matches the shape the real /login handler would
// write.
func newSessionCookie(t *testing.T, sm *SessionManager, u *User) *http.Cookie {
	t.Helper()
	ctx, err := sm.SessionManager.Load(context.Background(), "")
	require.NoError(t, err)
	sm.PutUser(ctx, u)
	token, _, err := sm.SessionManager.Commit(ctx)
	require.NoError(t, err)
	return &http.Cookie{Name: sm.Cookie.Name, Value: token}
}

// runMiddleware wires the middleware to a sentinel downstream handler and
// returns the User the handler saw plus the recorded response. sawUser is
// nil when the request was rejected before reaching the downstream handler.
func runMiddleware(provider *BasicProvider, req *http.Request) (sawUser *User, rec *httptest.ResponseRecorder) {
	rec = httptest.NewRecorder()
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	provider.Middleware(downstream).ServeHTTP(rec, req)
	return
}

// TestBasicMiddleware_SessionWinsOverPeerAuth_LocalAPI is the regression case
// that caused the Scrier 519 bug: a browser running on the server host POSTs
// an API request with a valid session cookie. Before the fix, peer-auth
// short-circuited to the synthetic "localhost" admin. After the fix, the
// real session user wins.
func TestBasicMiddleware_SessionWinsOverPeerAuth_LocalAPI(t *testing.T) {
	provider, sm, _, _, alice := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52000" // local
	req.AddCookie(newSessionCookie(t, sm, alice))

	got, rec := runMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username, "local API request with session cookie must resolve to the session user, not the loopback admin")
}

// TestBasicMiddleware_TokenWinsOverPeerAuth_LocalAPI is the parallel latent
// bug: a bearer token from localhost should identify the token's user, not
// the synthetic "localhost" admin. Audit records were previously wrong.
func TestBasicMiddleware_TokenWinsOverPeerAuth_LocalAPI(t *testing.T) {
	provider, _, _, tokenStore, alice := setupBasicProvider(t)

	plain, _, err := tokenStore.CreateToken(context.Background(), alice.ID, "test-token", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52001"
	req.Header.Set("Authorization", "Bearer "+plain)

	got, rec := runMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username, "local API request with valid bearer token must resolve to the token user, not the loopback admin")
}

// TestBasicMiddleware_NoAuth_LocalAPI_FallsToPeerAuth confirms the CLI path
// still works: vigocli sends no cookie and no bearer token, arrives from
// loopback or the Docker bridge, and must land on the implicit admin.
func TestBasicMiddleware_NoAuth_LocalAPI_FallsToPeerAuth(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52002"

	got, rec := runMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "localhost", got.Username, "CLI (no cookie, no token, from loopback) must land on peer-auth loopback admin")
	assert.Equal(t, "admin", got.Role)
}

// TestBasicMiddleware_NoAuth_DockerBridge_FallsToPeerAuth confirms peer-auth
// also fires for requests from the Docker bridge (172.16/12). This is what
// happens when vigocli runs on the server host but inside the vigo container
// talks back out via the bridge gateway IP.
func TestBasicMiddleware_NoAuth_DockerBridge_FallsToPeerAuth(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "172.17.0.1:40000" // Docker bridge gateway

	got, rec := runMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "localhost", got.Username)
}

// TestBasicMiddleware_NoAuth_RemoteAPI_Returns401 confirms remote requests
// without any credential are rejected (peer-auth must NOT fire for
// non-local addresses).
func TestBasicMiddleware_NoAuth_RemoteAPI_Returns401(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "8.8.8.8:40000"

	got, rec := runMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Nil(t, got, "downstream handler must not run for unauthenticated remote API requests")
}

// TestBasicMiddleware_Session_RemoteAPI confirms a remote request with a
// valid session cookie resolves to the session user exactly as before.
func TestBasicMiddleware_Session_RemoteAPI(t *testing.T) {
	provider, sm, _, _, alice := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "8.8.8.8:40000"
	req.AddCookie(newSessionCookie(t, sm, alice))

	got, rec := runMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username)
}

// TestBasicMiddleware_NoAuth_LocalUIPath_RedirectsLogin confirms peer-auth
// is API-only. A local browser hitting a UI path without a session still
// bounces to /login (not silently granted loopback admin).
func TestBasicMiddleware_NoAuth_LocalUIPath_RedirectsLogin(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52003"

	got, rec := runMiddleware(provider, req)
	assert.Equal(t, http.StatusFound, rec.Code, "unauthenticated UI request should redirect")
	assert.Equal(t, "/login", rec.Header().Get("Location"))
	assert.Nil(t, got)
}

// TestBasicMiddleware_PublicPath_Skipped confirms public paths never touch
// any auth branch — no cookie check, no token check, no peer-auth.
func TestBasicMiddleware_PublicPath_Skipped(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/static/css/style.css", nil)
	req.RemoteAddr = "8.8.8.8:40000"

	got, rec := runMiddleware(provider, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, got, "public paths must not populate the user context")
}

// TestBasicMiddleware_InvalidToken_API_Returns401 confirms an invalid bearer
// token short-circuits to 401 for API paths — it does NOT silently fall
// through to peer-auth. A client that sent a bad token meant to use a
// specific identity; falling through to the loopback admin would mask the
// error.
func TestBasicMiddleware_InvalidToken_API_Returns401(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52004" // local — peer-auth would otherwise apply
	req.Header.Set("Authorization", "Bearer vgot_invalid")

	got, rec := runMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "invalid bearer token must 401, not silently fall through to peer-auth")
	assert.Nil(t, got)
}
