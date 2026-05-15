// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"

	"git.alexander4.io/vigo/server/secrets"
)

// auditRecorder is the interface for the tamper-evident audit writer.
type auditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// recordAudit is a nil-safe helper that records an audit event.
func recordAudit(ctx context.Context, w auditRecorder, eventType, actor, source, summary, payload string) {
	if w == nil {
		return
	}
	if err := w.Record(ctx, eventType, actor, source, summary, payload); err != nil {
		slog.Error("audit record failed", "type", eventType, "error", err)
	}
}

// BasicProvider implements Provider using username/password authentication.
// User accounts live in the database; passwords are stored verbatim in the
// secrets provider at vigo/web/auth/<username> under the master-key
// envelope. The unlock-gated vault is the operator-side boundary; this
// handler does a constant-time compare against the resolved value on every
// login attempt. An account is not active until a password secret is set.
type BasicProvider struct {
	sessions    *SessionManager
	userStore   *UserStore
	tokenStore  *TokenStore
	secrets     secrets.Provider
	auditWriter auditRecorder
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

// SetAuditWriter sets the audit writer for recording auth events.
func (p *BasicProvider) SetAuditWriter(w auditRecorder) { p.auditWriter = w }

func (p *BasicProvider) Method() string { return "basic" }

// Middleware checks for a valid session or API token.
// Unauthenticated UI requests redirect to /login; API requests get 401.
func (p *BasicProvider) Middleware(next http.Handler) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		// Fallback: trust local API requests — CLI on the server host
		// gets implicit admin access (same as PostgreSQL peer auth).
		// Placed AFTER token + session checks so a real user's cookie or
		// token takes precedence. Without this ordering, a browser
		// running on the server host is authenticated as the synthetic
		// loopback admin instead of its logged-in session user, and any
		// API endpoint that keys on user identity (Scrier session
		// ownership, audit trail, per-user queries) misattributes the
		// request. Shared helper lives in auth.go so oidc.go and
		// isowebauth.go call the same code path.
		if u := TryPeerAuth(r); u != nil {
			ctx := ContextWithUser(r.Context(), u)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if isAPIPath(r.URL.Path) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
			return
		}

		http.Redirect(w, r, "/login", http.StatusFound)
	})

	// Wrap with SCS session management, but skip LoadAndSave for WebSocket
	// upgrades — SCS wraps the ResponseWriter which strips http.Hijacker.
	// For WebSocket, load session data read-only without wrapping.
	sessionHandler := p.sessions.LoadAndSave(inner)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			ctx := p.sessions.LoadSessionReadOnly(r)
			inner.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		sessionHandler.ServeHTTP(w, r)
	})
}

// RegisterRoutes mounts /login, /logout, and /totp.
func (p *BasicProvider) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", p.handleLoginPage)
	mux.HandleFunc("POST /login", p.handleLogin)
	mux.HandleFunc("GET /totp", p.handleTOTPPage)
	mux.HandleFunc("POST /totp", p.handleTOTPVerify)
	mux.HandleFunc("POST /logout", p.handleLogout)
}

func (p *BasicProvider) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// If already logged in, redirect to dashboard.
	user := p.sessions.GetUser(r.Context())
	if user != nil {
		http.Redirect(w, r, "/", http.StatusFound)
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
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", username, "auth", "unknown user", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		serveLoginPage(w, "Invalid username or password")
		return
	}

	// Resolve the stored password from the secrets provider. The Provider
	// is unattended (Get is direct, no operator interaction), so vigosrv
	// keeps authenticating logins across restarts without ever touching the
	// unlock gate.
	secretKey := fmt.Sprintf("vigo/web/auth/%s", username)
	stored, err := p.secrets.Get(r.Context(), secretKey)
	if err != nil {
		slog.Debug("no password secret for user", "username", username, "error", err)
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", username, "auth", "no password configured", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		serveLoginPage(w, "Invalid username or password")
		return
	}
	if stored == "" {
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", username, "auth", "no password configured", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		serveLoginPage(w, "Invalid username or password")
		return
	}

	// Constant-time compare against the stored value.
	if subtle.ConstantTimeCompare([]byte(stored), []byte(password)) != 1 {
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", username, "auth", "bad password", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		serveLoginPage(w, "Invalid username or password")
		return
	}

	// If TOTP is enabled, park credentials in session and redirect to challenge.
	if user.TOTPEnabled {
		if err := p.sessions.RenewToken(r.Context()); err != nil {
			slog.Error("session renewal failed", "error", err)
		}
		p.sessions.Put(r.Context(), "totp_pending_user_id", user.ID)
		p.sessions.Put(r.Context(), "totp_pending_username", user.Username)
		http.Redirect(w, r, "/totp", http.StatusFound)
		return
	}

	// Create session (no TOTP).
	if err := p.sessions.RenewToken(r.Context()); err != nil {
		slog.Error("session renewal failed", "error", err)
	}
	p.sessions.PutUser(r.Context(), user)

	recordAudit(r.Context(), p.auditWriter, "auth.login", username, "auth", "basic login", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
	http.Redirect(w, r, "/", http.StatusFound)
}

func (p *BasicProvider) handleTOTPPage(w http.ResponseWriter, r *http.Request) {
	// Only accessible if TOTP challenge is pending.
	userID := p.sessions.GetString(r.Context(), "totp_pending_user_id")
	if userID == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	serveTOTPPage(w, "")
}

func (p *BasicProvider) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		serveTOTPPage(w, "Invalid request")
		return
	}

	userID := p.sessions.GetString(r.Context(), "totp_pending_user_id")
	username := p.sessions.GetString(r.Context(), "totp_pending_username")
	if userID == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	code := r.FormValue("code")
	if code == "" {
		serveTOTPPage(w, "Code is required")
		return
	}

	// Look up user and TOTP secret.
	user, err := p.userStore.GetByID(r.Context(), userID)
	if err != nil || user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	// Get TOTP secret from secrets provider.
	totpSecret, err := p.secrets.Get(r.Context(), fmt.Sprintf("vigo/web/auth/totp/%s", user.Username))
	if err != nil || totpSecret == "" {
		slog.Error("totp secret not found", "username", user.Username, "error", err)
		serveTOTPPage(w, "TOTP configuration error")
		return
	}

	// Try TOTP code first.
	if ValidateTOTPCode(code, totpSecret) {
		// Clear TOTP pending state and complete login.
		p.sessions.Remove(r.Context(), "totp_pending_user_id")
		p.sessions.Remove(r.Context(), "totp_pending_username")
		p.sessions.PutUser(r.Context(), user)
		recordAudit(r.Context(), p.auditWriter, "auth.login", username, "auth", "basic login (totp)", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	// Try recovery code.
	recoveryJSON, _ := p.userStore.GetRecoveryCodes(r.Context(), userID)
	if valid, updatedJSON := ValidateRecoveryCode(code, recoveryJSON); valid {
		// Update recovery codes (remove used one).
		if err := p.userStore.UpdateRecoveryCodes(r.Context(), userID, updatedJSON); err != nil {
			slog.Error("failed to invalidate used recovery code", "user", username, "error", err)
		}
		p.sessions.Remove(r.Context(), "totp_pending_user_id")
		p.sessions.Remove(r.Context(), "totp_pending_username")
		p.sessions.PutUser(r.Context(), user)
		recordAudit(r.Context(), p.auditWriter, "auth.login", username, "auth", "basic login (recovery code)", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	recordAudit(r.Context(), p.auditWriter, "auth.login_failed", username, "auth", "bad totp code", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
	serveTOTPPage(w, "Invalid code")
}

func (p *BasicProvider) handleLogout(w http.ResponseWriter, r *http.Request) {
	user := p.sessions.GetUser(r.Context())
	actor := ""
	if user != nil {
		actor = user.Username
	}
	_ = p.sessions.DestroyUser(r.Context())
	recordAudit(r.Context(), p.auditWriter, "auth.logout", actor, "auth", "logout", "")
	http.Redirect(w, r, "/login", http.StatusFound)
}

// serveLoginPage renders the login form with an optional error message.
func serveLoginPage(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Vigo - Sign In</title>
    <link rel="stylesheet" href="/static/css/style.css">
</head>
<body>
    <div class="login-container">
        <div class="login-card">
            <img src="/static/img/logo.svg" alt="Vigo" style="height:36px;margin-bottom:0.5rem">
            <p class="login-subtitle">Sign in to continue</p>`))
	if errMsg != "" {
		w.Write([]byte(`<p class="login-error" style="color:#e74c3c;margin-bottom:1rem;text-align:center">` + html.EscapeString(errMsg) + `</p>`))
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

// serveTOTPPage renders the TOTP challenge form.
func serveTOTPPage(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Vigo - Two-Factor Authentication</title>
    <link rel="stylesheet" href="/static/css/style.css">
</head>
<body>
    <div class="login-container">
        <div class="login-card">
            <img src="/static/img/logo.svg" alt="Vigo" style="height:36px;margin-bottom:0.5rem">
            <p class="login-subtitle">Two-factor authentication</p>`))
	if errMsg != "" {
		w.Write([]byte(`<p class="login-error" style="color:#e74c3c;margin-bottom:1rem;text-align:center">` + html.EscapeString(errMsg) + `</p>`))
	}
	w.Write([]byte(`
            <form method="POST" action="/totp">
                <div class="form-group">
                    <label for="code">Authentication code</label>
                    <input type="text" id="code" name="code" required autofocus
                           class="form-input" placeholder="6-digit code or recovery code"
                           autocomplete="one-time-code" inputmode="numeric" pattern="[0-9A-Fa-f]*">
                </div>
                <button type="submit" class="btn btn--primary btn--full">Verify</button>
            </form>
            <p style="color:#6c757d;text-align:center;font-size:0.85rem;margin-top:1rem">
                Enter the code from your authenticator app, or a recovery code.
            </p>
        </div>
    </div>
</body>
</html>`))
}

