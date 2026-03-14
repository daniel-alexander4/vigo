// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hobnob/hobnob/server/db"
	"golang.org/x/oauth2"
)

// OIDCProvider implements Provider using OpenID Connect Authorization Code Flow.
type OIDCProvider struct {
	sessions   *SessionManager
	userStore  *UserStore
	tokenStore *TokenStore
	oauth2Cfg  *oauth2.Config
	verifier   *oidc.IDTokenVerifier
}

// OIDCConfig holds the OIDC configuration from server.yaml.
type OIDCConfig struct {
	Issuer       string   `yaml:"issuer"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	RedirectURL  string   `yaml:"redirect_url"`
	Scopes       []string `yaml:"scopes"`
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
		sessions:   sessions,
		userStore:  userStore,
		tokenStore: tokenStore,
		oauth2Cfg:  oauth2Cfg,
		verifier:   verifier,
	}, nil
}

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
	p.sessions.Put(r.Context(), "oauth_state", state)
	http.Redirect(w, r, p.oauth2Cfg.AuthCodeURL(state), http.StatusFound)
}

func (p *OIDCProvider) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Verify state.
	expectedState := p.sessions.PopString(r.Context(), "oauth_state")
	if r.URL.Query().Get("state") != expectedState || expectedState == "" {
		http.Error(w, "invalid state parameter", http.StatusBadRequest)
		return
	}

	// Exchange code for tokens.
	oauth2Token, err := p.oauth2Cfg.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		slog.Error("oidc code exchange failed", "error", err)
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}

	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "no id_token in response", http.StatusUnauthorized)
		return
	}

	idToken, err := p.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		slog.Error("oidc token verification failed", "error", err)
		http.Error(w, "token verification failed", http.StatusUnauthorized)
		return
	}

	// Extract claims.
	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
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
		// First-user-becomes-admin.
		role := "viewer"
		adminCount, countErr := p.userStore.CountAdmins(r.Context())
		if countErr != nil {
			slog.Error("admin count failed", "error", countErr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if adminCount == 0 {
			role = "admin"
			slog.Info("first OIDC login — auto-promoting to admin", "subject", claims.Subject)
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
	} else {
		// Update display name and email from IdP on each login.
		_ = p.userStore.Update(r.Context(), db.UpdateUserParams{
			ID:          user.ID,
			DisplayName: claims.Name,
			Email:       claims.Email,
			Role:        user.Role,
		})
		user.DisplayName = claims.Name
		user.Email = claims.Email
	}

	// Create session.
	if err := p.sessions.RenewToken(r.Context()); err != nil {
		slog.Error("session renewal failed", "error", err)
	}
	p.sessions.PutUser(r.Context(), user)

	http.Redirect(w, r, "/", http.StatusFound)
}

func (p *OIDCProvider) handleLogout(w http.ResponseWriter, r *http.Request) {
	_ = p.sessions.DestroyUser(r.Context())
	http.Redirect(w, r, "/login", http.StatusFound)
}

func generateRandomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
