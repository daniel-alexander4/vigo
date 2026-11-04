// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"git.alexander4.io/vigo/server/db"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCProvider implements Provider using OpenID Connect Authorization Code Flow.
type OIDCProvider struct {
	sessions    *SessionManager
	userStore   *UserStore
	tokenStore  *TokenStore
	oauth2Cfg   *oauth2.Config
	verifier    *oidc.IDTokenVerifier
	auditWriter auditRecorder
	// bootstrapAdminEmail/Subject name the single OIDC identity provisioned
	// as admin on first login. Both empty = no auto-admin (the default).
	bootstrapAdminEmail   string
	bootstrapAdminSubject string
}

// OIDCConfig holds the OIDC configuration from server.yaml.
type OIDCConfig struct {
	Issuer       string   `yaml:"issuer"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	RedirectURL  string   `yaml:"redirect_url"`
	Scopes       []string `yaml:"scopes"`

	BootstrapAdminEmail   string `yaml:"bootstrap_admin_email"`
	BootstrapAdminSubject string `yaml:"bootstrap_admin_subject"`
}

// NewOIDCProvider discovers the OIDC endpoints from the issuer and returns a
// configured provider. Returns an error if discovery fails.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig, sessions *SessionManager, userStore *UserStore, tokenStore *TokenStore) (*OIDCProvider, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}

	oauth2Cfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       scopes,
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})

	return &OIDCProvider{
		sessions:              sessions,
		userStore:             userStore,
		tokenStore:            tokenStore,
		oauth2Cfg:             oauth2Cfg,
		verifier:              verifier,
		bootstrapAdminEmail:   cfg.BootstrapAdminEmail,
		bootstrapAdminSubject: cfg.BootstrapAdminSubject,
	}, nil
}

// flexBool unmarshals an OIDC claim that may arrive as a JSON boolean
// (`true`) or, from some IdPs (e.g. Keycloak), a JSON string ("true").
// Plain `bool` would fail the whole claims unmarshal on the string form
// and break login; this keeps the well-behaved-bool case identical while
// tolerating the string case. Any non-true value (including a malformed
// claim) reads as false — fail-closed for a security-gating flag.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "true", `"true"`:
		*b = true
	default:
		*b = false
	}
	return nil
}

// isBootstrapAdmin reports whether the OIDC identity (subject/email) matches
// the configured bootstrap admin. Both config fields empty ⇒ never matches,
// so new users default to viewer and admin is granted explicitly via
// `vigocli webusers set-role`. This replaces the prior "first OIDC login when
// admin count is zero" rule, which silently promoted whoever logged in first
// on an IdP that allows self-registration.
func (p *OIDCProvider) isBootstrapAdmin(subject, email string, emailVerified bool) bool {
	if p.bootstrapAdminSubject != "" && subject == p.bootstrapAdminSubject {
		return true
	}
	// Email matching requires the IdP to have verified the address. Without
	// this check, any IdP that lets a user self-set an unverified email would
	// let an attacker claim the configured admin email and be provisioned as
	// admin on first login. Subject matching is unaffected — the subject is
	// IdP-assigned and stable.
	if p.bootstrapAdminEmail != "" && emailVerified && email == p.bootstrapAdminEmail {
		return true
	}
	return false
}

// SetAuditWriter sets the audit writer for recording auth events.
func (p *OIDCProvider) SetAuditWriter(w auditRecorder) { p.auditWriter = w }

// Sessions exposes the SCS session manager so handlers that need to
// invalidate cached sessions (e.g. on role change or user delete) can
// reach it without a parallel construction path.
func (p *OIDCProvider) Sessions() *SessionManager { return p.sessions }

// UserStore returns the provider's user store (see BasicProvider.UserStore).
func (p *OIDCProvider) UserStore() *UserStore { return p.userStore }

func (p *OIDCProvider) Method() string { return "oidc" }

// Middleware checks for a valid session or API token. Unauthenticated UI
// requests redirect to /login; unauthenticated API requests get 401.
func (p *OIDCProvider) Middleware(next http.Handler) http.Handler {
	return p.sessions.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Check API token first.
		if token := ExtractBearerToken(r.Header.Get("Authorization")); token != "" {
			user, err := p.tokenStore.ValidateAndResolve(r.Context(), token, p.userStore)
			if err != nil {
				slog.Error("api token validation error", "error", err)
			}
			if user != nil {
				ctx := ContextWithUser(r.Context(), user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if isAPIPath(r.URL.Path) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid or expired token"})
				return
			}
		}

		// Check session.
		user := p.sessions.GetUser(r.Context())
		if user != nil {
			ctx := ContextWithUser(r.Context(), user)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Not authenticated.
		if isAPIPath(r.URL.Path) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
			return
		}

		http.Redirect(w, r, "/login", http.StatusFound)
	}))
}

// RegisterRoutes mounts /login, /auth/callback, and /logout.
func (p *OIDCProvider) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", p.handleLogin)
	mux.HandleFunc("GET /auth/callback", p.handleCallback)
	mux.HandleFunc("POST /logout", p.handleLogout)
}

func (p *OIDCProvider) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := generateRandomState()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	nonce, err := generateRandomState()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// PKCE (RFC 7636, S256) binds the authorization code to this browser
	// session: a code intercepted on the redirect cannot be redeemed
	// without the verifier, which never leaves the server. The nonce binds
	// the ID token to this login attempt, so a token minted for another
	// flow cannot be replayed into this one.
	verifier := oauth2.GenerateVerifier()
	p.sessions.Put(r.Context(), "oauth_state", state)
	p.sessions.Put(r.Context(), "oauth_verifier", verifier)
	p.sessions.Put(r.Context(), "oauth_nonce", nonce)
	http.Redirect(w, r, p.oauth2Cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), http.StatusFound)
}

func (p *OIDCProvider) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Verify state.
	// Pop all three login-attempt values together so none outlives the
	// attempt, whatever the outcome.
	expectedState := p.sessions.PopString(r.Context(), "oauth_state")
	verifier := p.sessions.PopString(r.Context(), "oauth_verifier")
	expectedNonce := p.sessions.PopString(r.Context(), "oauth_nonce")
	if r.URL.Query().Get("state") != expectedState || expectedState == "" {
		http.Error(w, "invalid state parameter", http.StatusBadRequest)
		return
	}
	if verifier == "" || expectedNonce == "" {
		http.Error(w, "login attempt expired — start again", http.StatusBadRequest)
		return
	}

	// Exchange code for tokens, proving possession of the PKCE verifier.
	oauth2Token, err := p.oauth2Cfg.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		slog.Error("oidc code exchange failed", "error", err)
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", "oidc", "auth", "code exchange failed", fmt.Sprintf(`{"ip":%q,"error":%q}`, r.RemoteAddr, err.Error()))
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}

	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", "oidc", "auth", "no id_token in response", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		http.Error(w, "no id_token in response", http.StatusUnauthorized)
		return
	}

	idToken, err := p.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		slog.Error("oidc token verification failed", "error", err)
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", "oidc", "auth", "token verification failed", fmt.Sprintf(`{"ip":%q,"error":%q}`, r.RemoteAddr, err.Error()))
		http.Error(w, "token verification failed", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(expectedNonce)) != 1 {
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", "oidc", "auth", "id_token nonce mismatch", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		http.Error(w, "token verification failed", http.StatusUnauthorized)
		return
	}

	// Extract claims.
	var claims struct {
		Subject       string   `json:"sub"`
		Email         string   `json:"email"`
		EmailVerified flexBool `json:"email_verified"`
		Name          string   `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		slog.Error("oidc claims extraction failed", "error", err)
		http.Error(w, "claims extraction failed", http.StatusInternalServerError)
		return
	}

	// Upsert user: look up by OIDC subject, create if not found.
	user, err := p.userStore.GetByOIDCSubject(r.Context(), claims.Subject)
	if err != nil {
		slog.Error("user lookup failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if user == nil {
		// New users default to viewer. Admin is granted only to the single
		// configured bootstrap identity (opt-in); otherwise an operator
		// promotes via `vigocli webusers set-role` on the server host.
		role := "viewer"
		if p.isBootstrapAdmin(claims.Subject, claims.Email, bool(claims.EmailVerified)) {
			role = "admin"
			slog.Info("OIDC bootstrap admin matched — provisioning as admin", "subject", claims.Subject)
		}

		username := claims.Email
		if username == "" {
			username = claims.Subject
		}

		user, err = p.userStore.Create(r.Context(), db.InsertUserParams{
			Username:    username,
			DisplayName: claims.Name,
			Email:       claims.Email,
			Role:        role,
			OidcSubject: claims.Subject,
		})
		if err != nil {
			slog.Error("user creation failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if role == "admin" {
			recordAudit(r.Context(), p.auditWriter, "user.role_change", username, "auth", fmt.Sprintf("%s provisioned as admin via OIDC bootstrap", username), fmt.Sprintf(`{"subject":%q}`, claims.Subject))
		}
	} else if claims.Name != user.DisplayName || claims.Email != user.Email {
		// Refresh display name and email from the IdP when they changed.
		// Every other column is carried over as stored: UpdateUser writes
		// all of them, so passing role defaults or an empty key here would
		// reset the user's permissions and wipe their SSH pubkey (which
		// backs scrier) on every login. Skipping the no-change case also
		// keeps a plain login from re-signing the row and churning the
		// spanner replication comparator.
		perms, _ := json.Marshal(user.Permissions)
		if err := p.userStore.Update(r.Context(), db.UpdateUserParams{
			ID:           user.ID,
			DisplayName:  claims.Name,
			Email:        claims.Email,
			Role:         user.Role,
			Permissions:  string(perms),
			SshPublicKey: user.SSHPublicKey,
		}); err != nil {
			// The login itself is valid; a stale profile is not a reason
			// to refuse it. Surface the failure instead of discarding it.
			slog.Error("oidc profile refresh failed", "user", user.Username, "error", err)
		} else {
			user.DisplayName = claims.Name
			user.Email = claims.Email
		}
	}

	// Create session.
	if err := p.sessions.RenewToken(r.Context()); err != nil {
		slog.Error("session renewal failed", "error", err)
	}
	p.sessions.PutUser(r.Context(), user)

	recordAudit(r.Context(), p.auditWriter, "auth.login", user.Username, "auth", "oidc login", fmt.Sprintf(`{"ip":%q,"subject":%q}`, r.RemoteAddr, claims.Subject))
	http.Redirect(w, r, "/", http.StatusFound)
}

func (p *OIDCProvider) handleLogout(w http.ResponseWriter, r *http.Request) {
	user := p.sessions.GetUser(r.Context())
	actor := ""
	if user != nil {
		actor = user.Username
	}
	_ = p.sessions.DestroyUser(r.Context())
	recordAudit(r.Context(), p.auditWriter, "auth.logout", actor, "auth", "logout", "")
	http.Redirect(w, r, "/login", http.StatusFound)
}

func generateRandomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
