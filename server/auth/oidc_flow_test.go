// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"git.alexander4.io/vigo/server/db"
)

// fakeIdP is a minimal OIDC token endpoint. It remembers the PKCE
// challenge and nonce the authorization request carried, refuses a code
// redemption whose code_verifier does not hash to the challenge, and mints
// an RS256 ID token carrying the nonce (or overrideNonce when set).
type fakeIdP struct {
	srv           *httptest.Server
	key           *rsa.PrivateKey
	mu            sync.Mutex
	challenge     string
	nonce         string
	overrideNonce string
	subject       string
	name          string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	idp := &fakeIdP{key: key, subject: "sub-1", name: "Alice"}
	idp.srv = httptest.NewServer(http.HandlerFunc(idp.token))
	t.Cleanup(idp.srv.Close)
	return idp
}

func (f *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if f.challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"PKCE verification failed"}`))
		return
	}
	nonce := f.nonce
	if f.overrideNonce != "" {
		nonce = f.overrideNonce
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"access_token": "at",
		"token_type":   "Bearer",
		"expires_in":   300,
		"id_token":     f.idToken(nonce),
	})
}

func (f *fakeIdP) idToken(nonce string) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	now := time.Now().Unix()
	signing := enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + enc(map[string]any{
		"iss": f.srv.URL, "aud": "vigo", "sub": f.subject, "exp": now + 300, "iat": now,
		"nonce": nonce, "name": f.name, "email": "alice@example.com", "email_verified": true,
	})
	h := sha256.Sum256([]byte(signing))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, h[:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// oidcFlow wires an OIDCProvider to the fake IdP and returns a handler
// with the session middleware in front of its routes.
func oidcFlow(t *testing.T, idp *fakeIdP) (*OIDCProvider, http.Handler) {
	t.Helper()
	p, sm, _ := setupOIDCProvider(t)
	p.oauth2Cfg = &oauth2.Config{
		ClientID:    "vigo",
		Endpoint:    oauth2.Endpoint{AuthURL: idp.srv.URL + "/auth", TokenURL: idp.srv.URL + "/token"},
		RedirectURL: "https://vigo.example/auth/callback",
		Scopes:      []string{oidc.ScopeOpenID},
	}
	p.verifier = oidc.NewVerifier(idp.srv.URL, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&idp.key.PublicKey}}, &oidc.Config{ClientID: "vigo"})
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	return p, sm.LoadAndSave(mux)
}

// runOIDCLogin performs /login, records what the authorization request
// carried at the IdP, then follows /auth/callback. Returns the callback
// response.
func runOIDCLogin(t *testing.T, idp *fakeIdP, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, "/login", nil))
	require.Equal(t, http.StatusFound, loginRec.Code)
	loc, err := url.Parse(loginRec.Header().Get("Location"))
	require.NoError(t, err)
	q := loc.Query()

	idp.mu.Lock()
	idp.challenge = ""
	if q.Get("code_challenge_method") == "S256" {
		idp.challenge = q.Get("code_challenge")
	}
	idp.nonce = q.Get("nonce")
	idp.mu.Unlock()

	cb := httptest.NewRequest(http.MethodGet, "/auth/callback?state="+url.QueryEscape(q.Get("state"))+"&code=c1", nil)
	for _, c := range loginRec.Result().Cookies() {
		cb.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, cb)
	return rec
}

// TestOIDCLogin_PKCEAndNonce: the authorization request carries an S256
// PKCE challenge and a nonce, the code exchange proves the verifier (the
// fake IdP refuses otherwise), and the login completes.
func TestOIDCLogin_PKCEAndNonce(t *testing.T) {
	idp := newFakeIdP(t)
	p, h := oidcFlow(t, idp)

	rec := runOIDCLogin(t, idp, h)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/", rec.Header().Get("Location"))
	assert.NotEmpty(t, idp.challenge, "authorization request must carry an S256 code_challenge")
	assert.NotEmpty(t, idp.nonce, "authorization request must carry a nonce")

	u, err := p.userStore.GetByOIDCSubject(context.Background(), "sub-1")
	require.NoError(t, err)
	require.NotNil(t, u)
}

// TestOIDCLogin_NonceMismatchRejected: an ID token whose nonce is not the
// one this login attempt issued is refused.
func TestOIDCLogin_NonceMismatchRejected(t *testing.T) {
	idp := newFakeIdP(t)
	idp.overrideNonce = "someone-elses-nonce"
	_, h := oidcFlow(t, idp)

	rec := runOIDCLogin(t, idp, h)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestOIDCLogin_RefreshKeepsStoredFields: a returning user whose IdP name
// changed gets the new name, while their SSH pubkey and permissions stay
// as stored (UpdateUser writes every column).
func TestOIDCLogin_RefreshKeepsStoredFields(t *testing.T) {
	idp := newFakeIdP(t)
	p, h := oidcFlow(t, idp)
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBTnhQDY+EoHzuH6lTBahbzZVGcRcGEjhBBg0CLVK+cd bob@vigo"
	_, err := p.userStore.Create(context.Background(), db.InsertUserParams{
		Username: "bob", Role: "viewer", OidcSubject: "sub-1", SshPublicKey: key, DisplayName: "Old Name",
	})
	require.NoError(t, err)

	idp.name = "New Name"
	rec := runOIDCLogin(t, idp, h)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())

	u, err := p.userStore.GetByOIDCSubject(context.Background(), "sub-1")
	require.NoError(t, err)
	assert.Equal(t, "New Name", u.DisplayName)
	assert.Equal(t, key, u.SSHPublicKey, "an OIDC login must not wipe the stored SSH pubkey")
	assert.ElementsMatch(t, ViewerPermissions, u.Permissions)
}
