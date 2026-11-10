// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/httpsec"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/secrets"
	"git.alexander4.io/vigo/server/testutil"
)

// stubSecretsProvider returns canned values keyed by path. Used by the
// login-compare tests below — the rest of the file uses secrets.NullProvider
// because the middleware tests never read a password.
type stubSecretsProvider struct {
	values map[string]string
}

func (s stubSecretsProvider) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s stubSecretsProvider) GetBytes(_ context.Context, key string) ([]byte, error) {
	return []byte(s.values[key]), nil
}

func (stubSecretsProvider) Close() error { return nil }

// setupBasicProvider returns a fully-wired BasicProvider backed by a real
// SCS session store plus a seeded admin user. The real SessionManager lets
// tests round-trip session cookies exactly the way production does.
func setupBasicProvider(t *testing.T) (*BasicProvider, *SessionManager, *UserStore, *TokenStore, *User) {
	t.Helper()
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := db.New(sqlDB)

	sm := NewSessionManager(sqlDB, 0)
	userStore := NewUserStore(queries)
	tokenStore := NewTokenStore(queries, testLocalAdminToken, metrics.NewTestMetrics())

	// Seed an admin user so the auth middleware has something to authenticate
	// against. Provider uses NullProvider for secrets.
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

// TestBasicMiddleware_Session_LocalAPI confirms a session cookie on an API
// request resolves to the real session user.
func TestBasicMiddleware_Session_LocalAPI(t *testing.T) {
	provider, sm, _, _, alice := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52000" // local
	req.AddCookie(newSessionCookie(t, sm, alice))

	got, rec := runMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username, "an API request with a session cookie must resolve to the session user")
}

// TestBasicMiddleware_Token_LocalAPI confirms a bearer token resolves to the
// token's owning user.
func TestBasicMiddleware_Token_LocalAPI(t *testing.T) {
	provider, _, _, tokenStore, alice := setupBasicProvider(t)

	plain, _, err := tokenStore.CreateToken(context.Background(), alice.ID, "test-token", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52001"
	req.Header.Set("Authorization", "Bearer "+plain)

	got, rec := runMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username, "an API request with a valid bearer token must resolve to the token user")
}

// TestBasicMiddleware_NoAuth_LocalAPI_Returns401 is the regression for the
// peer-auth hole: a request from loopback with no credential must NOT be
// granted admin. Source IP is not proof of root — a non-root shell user on
// the server host could otherwise curl the API as admin.
func TestBasicMiddleware_NoAuth_LocalAPI_Returns401(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52002"

	got, rec := runMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "loopback alone must not grant admin")
	assert.Nil(t, got, "an uncredentialed loopback request must reach no downstream handler")
}

// TestBasicMiddleware_NoAuth_DockerBridge_Returns401 is the same regression
// for the Docker bridge (172.16/12) — the other range the retired source-IP
// peer auth trusted. It too must now 401 without a credential.
func TestBasicMiddleware_NoAuth_DockerBridge_Returns401(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "172.17.0.1:40000" // Docker bridge gateway

	got, rec := runMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the Docker bridge alone must not grant admin")
	assert.Nil(t, got)
}

// TestBasicMiddleware_LocalAdminToken_GrantsAdmin confirms the credential
// that replaced peer auth: a request bearing the boot-minted local-admin
// token resolves to the synthetic server-host admin — regardless of source
// IP, since the token, not the address, is the gate. This is how vigocli on
// the server host authenticates.
func TestBasicMiddleware_LocalAdminToken_GrantsAdmin(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "8.8.8.8:40000" // deliberately non-local: the token is the gate
	req.Header.Set("Authorization", "Bearer "+testLocalAdminToken)

	got, rec := runMiddleware(provider, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "localhost", got.Username)
	assert.Equal(t, "admin", got.Role)
}

// TestBasicMiddleware_NoAuth_RemoteAPI_Returns401 confirms a remote request
// without any credential is rejected.
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

// TestBasicMiddleware_NoAuth_LocalUIPath_RedirectsLogin confirms a browser
// hitting a UI path without a session bounces to /login.
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
// any auth branch — no cookie check, no token check.
func TestBasicMiddleware_PublicPath_Skipped(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/static/css/style.css", nil)
	req.RemoteAddr = "8.8.8.8:40000"

	got, rec := runMiddleware(provider, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, got, "public paths must not populate the user context")
}

// TestBasicMiddleware_InvalidToken_API_Returns401 confirms an invalid bearer
// token short-circuits to 401 for API paths rather than being treated as
// unauthenticated — a client that sent a bad token meant a specific identity.
func TestBasicMiddleware_InvalidToken_API_Returns401(t *testing.T) {
	provider, _, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/envoys", nil)
	req.RemoteAddr = "127.0.0.1:52004"
	req.Header.Set("Authorization", "Bearer vgot_invalid")

	got, rec := runMiddleware(provider, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "invalid bearer token must 401, not silently fall through to peer-auth")
	assert.Nil(t, got)
}

// setupBasicProviderWithSecrets is like setupBasicProvider but wires a
// stubSecretsProvider so the login-compare path can be exercised end-to-end.
func setupBasicProviderWithSecrets(t *testing.T, values map[string]string) *BasicProvider {
	t.Helper()
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := db.New(sqlDB)

	sm := NewSessionManager(sqlDB, 0)
	userStore := NewUserStore(queries)
	tokenStore := NewTokenStore(queries, testLocalAdminToken, metrics.NewTestMetrics())

	_, err := userStore.Create(context.Background(), db.InsertUserParams{
		Username: "alice",
		Role:     "admin",
	})
	require.NoError(t, err)

	return NewBasicProvider(sm, userStore, tokenStore, stubSecretsProvider{values: values})
}

func postLogin(t *testing.T, provider *BasicProvider, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	provider.RegisterRoutes(mux)
	provider.sessions.LoadAndSave(mux).ServeHTTP(rec, req)
	return rec
}

// mustHashPassword bcrypt-hashes a password for the vault, as the CLI write
// path does — the stored form basic auth now verifies against.
func mustHashPassword(t *testing.T, pw string) string {
	t.Helper()
	h, err := HashPassword(pw)
	require.NoError(t, err)
	return h
}

// TestBasicLogin_BcryptCompare_Succeeds: a correct password verifies against
// the stored bcrypt hash and produces a session cookie + redirect.
func TestBasicLogin_BcryptCompare_Succeeds(t *testing.T) {
	provider := setupBasicProviderWithSecrets(t, map[string]string{
		"vigo/web/auth/alice": mustHashPassword(t, "CorrectHorseBattery$tap1e!"),
	})

	rec := postLogin(t, provider, "alice", "CorrectHorseBattery$tap1e!")
	require.Equal(t, http.StatusFound, rec.Code, "successful login must redirect")
	require.NotEmpty(t, rec.Header().Get("Set-Cookie"), "successful login must set a session cookie")
}

// TestBasicLogin_BcryptCompare_WrongPasswordFails confirms a mismatched
// password is rejected without setting a session cookie, and the response is
// the login page (200), not a redirect.
func TestBasicLogin_BcryptCompare_WrongPasswordFails(t *testing.T) {
	provider := setupBasicProviderWithSecrets(t, map[string]string{
		"vigo/web/auth/alice": mustHashPassword(t, "CorrectHorseBattery$tap1e!"),
	})

	rec := postLogin(t, provider, "alice", "WrongHorseBattery$tap1e!")
	assert.Equal(t, http.StatusOK, rec.Code, "failed login must re-render the login page, not redirect")
	for _, c := range rec.Result().Cookies() {
		assert.NotEqual(t, "session", c.Name, "failed login must not set a session cookie")
	}
}

// TestBasicLogin_LegacyPlaintextFails pins the forced-reset migration: a
// pre-hash-at-rest plaintext value in the vault is NOT a bcrypt hash, so even
// the exact stored string fails to authenticate. The account must be reset via
// `vigocli webusers set-password`.
func TestBasicLogin_LegacyPlaintextFails(t *testing.T) {
	provider := setupBasicProviderWithSecrets(t, map[string]string{
		"vigo/web/auth/alice": "CorrectHorseBattery$tap1e!", // legacy plaintext, not a hash
	})

	rec := postLogin(t, provider, "alice", "CorrectHorseBattery$tap1e!")
	assert.Equal(t, http.StatusOK, rec.Code, "a legacy plaintext password must not authenticate")
	for _, c := range rec.Result().Cookies() {
		assert.NotEqual(t, "session", c.Name)
	}
}

// TestBasicLogin_NoPasswordStored_Fails confirms that a user with no entry in
// the secrets store cannot log in regardless of what's submitted.
func TestBasicLogin_NoPasswordStored_Fails(t *testing.T) {
	provider := setupBasicProviderWithSecrets(t, map[string]string{})

	rec := postLogin(t, provider, "alice", "anything")
	assert.Equal(t, http.StatusOK, rec.Code)
	for _, c := range rec.Result().Cookies() {
		assert.NotEqual(t, "session", c.Name)
	}
}

// TestLoginPages_CSPAndNoInlineStyles pins the login-page hardening (0.71.x):
// both the basic-auth login and TOTP pages must carry the locked CSP (no
// 'unsafe-inline') and contain zero inline style= attributes — they style
// exclusively via the shared stylesheet + classes.
func TestLoginPages_CSPAndNoInlineStyles(t *testing.T) {
	pages := map[string]func(http.ResponseWriter, string, string){
		"login": serveLoginPage,
		"totp":  serveTOTPPage,
	}
	for name, serve := range pages {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			serve(rec, "en", "auth.err.invalid_request") // exercise the error branch too
			res := rec.Result()

			csp := res.Header.Get("Content-Security-Policy")
			assert.Equal(t, httpsec.CSP, csp, "login page must carry the canonical locked CSP")
			assert.NotContains(t, csp, "unsafe-inline")
			assert.Equal(t, "SAMEORIGIN", res.Header.Get("X-Frame-Options"))
			assert.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))

			body := rec.Body.String()
			assert.NotContains(t, body, "style=", "no inline style attributes (de-inlined to classes)")
			assert.Contains(t, body, `href="/static/css/style.css"`, "styles via the shared stylesheet")
		})
	}
}
