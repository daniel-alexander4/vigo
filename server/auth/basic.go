// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/hobnob/hobnob/server/secrets"
	"golang.org/x/crypto/bcrypt"
)

// BasicProvider implements Provider using username/password authentication.
// User accounts live in the database; password hashes are stored in the
// local secrets provider at hobnob/web/auth/<username>. An account is not
// active until a password secret is set via hobnobcli.
type BasicProvider struct {
	sessions   *SessionManager
	userStore  *UserStore
	tokenStore *TokenStore
	secrets    secrets.Provider
}

// NewBasicProvider creates a BasicProvider.
func NewBasicProvider(sessions *SessionManager, userStore *UserStore, tokenStore *TokenStore, secretsProvider secrets.Provider) *BasicProvider {
	return &BasicProvider{
		sessions:   sessions,
		userStore:  userStore,
		tokenStore: tokenStore,
		secrets:    secretsProvider,
	}
}

func (p *BasicProvider) Method() string { return "basic" }

// Middleware checks for a valid session or API token. When no users exist,
// all non-public requests redirect to /setup (instructions page). Otherwise,
// unauthenticated UI requests redirect to /login; API requests get 401.
func (p *BasicProvider) Middleware(next http.Handler) http.Handler {
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

		// Not authenticated — check if setup is needed.
		count, err := p.userStore.CountUsers(r.Context())
		if err != nil {
			slog.Error("counting users in middleware", "error", err)
		}
		if count == 0 {
			if isAPIPath(r.URL.Path) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "setup required", "setup_url": "/setup"})
				return
			}
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}

		if isAPIPath(r.URL.Path) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
			return
		}

		http.Redirect(w, r, "/login", http.StatusFound)
	}))
}

// RegisterRoutes mounts /login, /logout, and /setup.
func (p *BasicProvider) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", p.handleLoginPage)
	mux.HandleFunc("POST /login", p.handleLogin)
	mux.HandleFunc("POST /logout", p.handleLogout)
	mux.HandleFunc("GET /setup", p.handleSetupPage)
}

func (p *BasicProvider) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// If already logged in, redirect to dashboard.
	user := p.sessions.GetUser(r.Context())
	if user != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	// If no users exist, redirect to setup.
	count, _ := p.userStore.CountUsers(r.Context())
	if count == 0 {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	serveLoginPage(w, "")
}

func (p *BasicProvider) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		serveLoginPage(w, "Invalid request")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		serveLoginPage(w, "Username and password are required")
		return
	}

	// Look up user in DB.
	user, err := p.userStore.GetByUsername(r.Context(), username)
	if err != nil {
		slog.Error("user lookup failed", "error", err)
		serveLoginPage(w, "An internal error occurred")
		return
	}
	if user == nil {
		serveLoginPage(w, "Invalid username or password")
		return
	}

	// Resolve password hash from secrets provider.
	secretKey := fmt.Sprintf("hobnob/web/auth/%s", username)
	hash, err := p.secrets.Get(r.Context(), secretKey)
	if err != nil {
		slog.Debug("no password secret for user", "username", username, "error", err)
		serveLoginPage(w, "Invalid username or password")
		return
	}
	if hash == "" {
		serveLoginPage(w, "Invalid username or password")
		return
	}

	// Compare password against bcrypt hash.
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		serveLoginPage(w, "Invalid username or password")
		return
	}

	// Create session.
	if err := p.sessions.RenewToken(r.Context()); err != nil {
		slog.Error("session renewal failed", "error", err)
	}
	p.sessions.PutUser(r.Context(), user)

	http.Redirect(w, r, "/", http.StatusFound)
}

func (p *BasicProvider) handleLogout(w http.ResponseWriter, r *http.Request) {
	_ = p.sessions.DestroyUser(r.Context())
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (p *BasicProvider) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	// Only show setup when no users exist.
	count, _ := p.userStore.CountUsers(r.Context())
	if count > 0 {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	serveSetupPage(w)
}

// serveLoginPage renders the login form with an optional error message.
func serveLoginPage(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Hobnob - Sign In</title>
    <link rel="stylesheet" href="/static/css/style.css">
</head>
<body>
    <div class="login-container">
        <div class="login-card">
            <h1 class="login-title">Hobnob</h1>
            <p class="login-subtitle">Sign in to continue</p>`))
	if errMsg != "" {
		w.Write([]byte(`<p class="login-error" style="color:#e74c3c;margin-bottom:1rem;text-align:center">` + errMsg + `</p>`))
	}
	w.Write([]byte(`
            <form method="POST" action="/login">
                <div class="form-group">
                    <label for="username">Username</label>
                    <input type="text" id="username" name="username" required autofocus
                           class="form-input" placeholder="Enter your username">
                </div>
                <div class="form-group">
                    <label for="password">Password</label>
                    <input type="password" id="password" name="password" required
                           class="form-input" placeholder="Enter your password">
                </div>
                <button type="submit" class="btn btn--primary btn--full">Sign In</button>
            </form>
        </div>
    </div>
</body>
</html>`))
}

// serveSetupPage renders the initial setup instructions.
func serveSetupPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Hobnob - Setup</title>
    <link rel="stylesheet" href="/static/css/style.css">
</head>
<body>
    <div class="login-container">
        <div class="login-card" style="max-width:540px">
            <h1 class="login-title">Hobnob</h1>
            <p class="login-subtitle">Initial Setup</p>
            <p style="color:#6c757d;text-align:center;margin-bottom:1.5rem;font-size:0.9rem">
                No user accounts exist yet. Use the CLI to create an admin account and set a password.
            </p>
            <div style="background:#1e1e2e;color:#cdd6f4;border-radius:6px;padding:1rem;font-family:monospace;font-size:0.85rem;line-height:1.6;margin-bottom:1rem">
                <div style="color:#6c7086;margin-bottom:0.5rem"># 1. Create the admin account</div>
                <div>hobnobcli users create \</div>
                <div style="padding-left:1.5rem">--username admin --role admin</div>
                <div style="color:#6c7086;margin-top:0.75rem;margin-bottom:0.5rem"># 2. Set the password</div>
                <div>hobnobcli users set-password \</div>
                <div style="padding-left:1.5rem">--username admin</div>
                <div style="color:#6c7086;font-size:0.8rem;margin-top:0.25rem">(prompts securely for the password)</div>
            </div>
            <hr style="border:none;border-top:1px solid #e0e0e0;margin:1.5rem 0">
            <p style="color:#6c757d;text-align:center;font-size:0.85rem">
                Once the account is created, <a href="/login">sign in here</a>.
            </p>
        </div>
    </div>
</body>
</html>`))
}
