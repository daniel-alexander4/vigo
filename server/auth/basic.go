// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/httpsec"
	"git.alexander4.io/vigo/server/i18n"
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
// User accounts live in the database; passwords are stored as a bcrypt hash in
// the secrets provider at vigo/web/auth/<username> under the master-key
// envelope. The unlock-gated vault is the operator-side boundary; this handler
// verifies the submitted password against the stored hash on every login
// attempt (a vault read yields only the hash, never plaintext). An account is
// not active until a password secret is set.
type BasicProvider struct {
	sessions    *SessionManager
	userStore   *UserStore
	tokenStore  *TokenStore
	secrets     secrets.Provider
	auditWriter auditRecorder
	i18n        config.I18nSection
	authLimiter *authLimiter
}

// NewBasicProvider creates a BasicProvider.
func NewBasicProvider(sessions *SessionManager, userStore *UserStore, tokenStore *TokenStore, secretsProvider secrets.Provider) *BasicProvider {
	return &BasicProvider{
		sessions:   sessions,
		userStore:  userStore,
		tokenStore: tokenStore,
		secrets:    secretsProvider,
		// 10 attempts/min per IP, burst 10 — invisible to a human mistyping a
		// password a few times, but throttles automated login + TOTP brute
		// force to a crawl. On by default; the HTTP auth path is otherwise
		// unthrottled (unlike the gRPC check-in path).
		authLimiter: newAuthLimiter(10, 10),
	}
}

// SetAuditWriter sets the audit writer for recording auth events.
func (p *BasicProvider) SetAuditWriter(w auditRecorder) { p.auditWriter = w }

// SetI18n sets the i18n config so the login/TOTP pages can resolve the
// per-request locale (cookie > Accept-Language > default).
func (p *BasicProvider) SetI18n(cfg config.I18nSection) { p.i18n = cfg }

// Sessions exposes the SCS session manager so handlers that need to
// invalidate cached sessions (e.g. on role change or user delete) can
// reach it without a parallel construction path.
func (p *BasicProvider) Sessions() *SessionManager { return p.sessions }

// UserStore returns the provider's user store. Wired by server.go to
// install the ADR-030 BoltSigner late — buildAuthProvider runs before
// the bolt identity is loaded, so the signer is set after construction.
func (p *BasicProvider) UserStore() *UserStore { return p.userStore }

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

// allowAuthAttempt applies the per-IP auth rate limit and audits a denial.
func (p *BasicProvider) allowAuthAttempt(r *http.Request) bool {
	return allowAuthAttempt(r, p.authLimiter, p.auditWriter)
}

// allowAuthAttempt is the per-IP auth rate limit shared by every provider
// that verifies a credential over HTTP (basic login + TOTP, isowebauth
// verify). Nil-guarded so tests that construct a bare provider aren't
// throttled.
func allowAuthAttempt(r *http.Request, limiter *authLimiter, audit auditRecorder) bool {
	if limiter == nil {
		return true
	}
	ip := clientIP(r)
	if limiter.allow(ip) {
		return true
	}
	recordAudit(r.Context(), audit, "auth.rate_limited", "", "auth", "auth attempt rate-limited", fmt.Sprintf(`{"ip":%q}`, ip))
	return false
}

func (p *BasicProvider) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// If already logged in, redirect to dashboard.
	user := p.sessions.GetUser(r.Context())
	if user != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	serveLoginPage(w, i18n.LocaleFromRequest(p.i18n, r), "")
}

func (p *BasicProvider) handleLogin(w http.ResponseWriter, r *http.Request) {
	locale := i18n.LocaleFromRequest(p.i18n, r)
	if !p.allowAuthAttempt(r) {
		http.Error(w, "too many authentication attempts — wait a minute and try again", http.StatusTooManyRequests)
		return
	}
	if err := r.ParseForm(); err != nil {
		serveLoginPage(w, locale, "auth.err.invalid_request")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		serveLoginPage(w, locale, "auth.err.credentials_required")
		return
	}

	// Look up user in DB.
	user, err := p.userStore.GetByUsername(r.Context(), username)
	if err != nil {
		slog.Error("user lookup failed", "error", err)
		serveLoginPage(w, locale, "auth.err.internal")
		return
	}
	if user == nil {
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", username, "auth", "unknown user", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		serveLoginPage(w, locale, "auth.err.invalid_credentials")
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
		serveLoginPage(w, locale, "auth.err.invalid_credentials")
		return
	}
	if stored == "" {
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", username, "auth", "no password configured", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		serveLoginPage(w, locale, "auth.err.invalid_credentials")
		return
	}

	// Verify against the stored bcrypt hash. A pre-hash-at-rest plaintext value
	// isn't a valid bcrypt hash and fails here — that account must be reset via
	// `vigocli webusers set-password`.
	if !VerifyPassword(stored, password) {
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", username, "auth", "bad password", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		serveLoginPage(w, locale, "auth.err.invalid_credentials")
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
	serveTOTPPage(w, i18n.LocaleFromRequest(p.i18n, r), "")
}

func (p *BasicProvider) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	locale := i18n.LocaleFromRequest(p.i18n, r)
	if !p.allowAuthAttempt(r) {
		http.Error(w, "too many authentication attempts — wait a minute and try again", http.StatusTooManyRequests)
		return
	}
	if err := r.ParseForm(); err != nil {
		serveTOTPPage(w, locale, "auth.err.invalid_request")
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
		serveTOTPPage(w, locale, "auth.err.code_required")
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
		serveTOTPPage(w, locale, "auth.err.totp_config")
		return
	}

	// Try TOTP code first. A code is accepted only if it both validates and
	// advances the last-consumed step — a replayed code validates but fails to
	// advance, so it falls through and does not authenticate.
	if ok, step := ValidateTOTPCodeStep(code, totpSecret, time.Now()); ok {
		if consumed, err := p.userStore.ConsumeTOTPStep(r.Context(), userID, step); err == nil && consumed {
			// Clear TOTP pending state and complete login.
			p.sessions.Remove(r.Context(), "totp_pending_user_id")
			p.sessions.Remove(r.Context(), "totp_pending_username")
			p.sessions.PutUser(r.Context(), user)
			recordAudit(r.Context(), p.auditWriter, "auth.login", username, "auth", "basic login (totp)", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
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
	serveTOTPPage(w, locale, "auth.err.invalid_code")
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

// serveLoginPage renders the login form. locale selects the catalog language;
// errKey is a catalog key ("" = no error) whose resolved text is shown in the
// error banner.
func serveLoginPage(w http.ResponseWriter, locale, errKey string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	httpsec.Headers(w)
	t := func(key string) string { return html.EscapeString(i18n.CatalogT(locale, key)) }
	w.Write([]byte(`<!DOCTYPE html>
<html lang="` + html.EscapeString(locale) + `">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>` + t("auth.login.title") + `</title>
    <link rel="stylesheet" href="/static/css/style.css">
</head>
<body>
    <div class="login-container">
        <div class="login-card">
            <img src="/static/img/logo.svg" alt="Vigo" class="login-logo">
            <p class="login-subtitle">` + t("auth.login.subtitle") + `</p>`))
	if errKey != "" {
		w.Write([]byte(`<p class="login-error">` + t(errKey) + `</p>`))
	}
	w.Write([]byte(`
            <form method="POST" action="/login">
                <div class="form-group">
                    <label for="username">` + t("auth.label.username") + `</label>
                    <input type="text" id="username" name="username" required autofocus
                           class="form-input" placeholder="` + t("auth.placeholder.username") + `">
                </div>
                <div class="form-group">
                    <label for="password">` + t("auth.label.password") + `</label>
                    <input type="password" id="password" name="password" required
                           class="form-input" placeholder="` + t("auth.placeholder.password") + `">
                </div>
                <button type="submit" class="btn btn--primary btn--full">` + t("auth.btn.sign_in") + `</button>
            </form>
        </div>
    </div>
</body>
</html>`))
}

// serveTOTPPage renders the TOTP challenge form. See serveLoginPage for the
// locale / errKey contract.
func serveTOTPPage(w http.ResponseWriter, locale, errKey string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	httpsec.Headers(w)
	t := func(key string) string { return html.EscapeString(i18n.CatalogT(locale, key)) }
	w.Write([]byte(`<!DOCTYPE html>
<html lang="` + html.EscapeString(locale) + `">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>` + t("auth.totp.title") + `</title>
    <link rel="stylesheet" href="/static/css/style.css">
</head>
<body>
    <div class="login-container">
        <div class="login-card">
            <img src="/static/img/logo.svg" alt="Vigo" class="login-logo">
            <p class="login-subtitle">` + t("auth.totp.subtitle") + `</p>`))
	if errKey != "" {
		w.Write([]byte(`<p class="login-error">` + t(errKey) + `</p>`))
	}
	w.Write([]byte(`
            <form method="POST" action="/totp">
                <div class="form-group">
                    <label for="code">` + t("auth.totp.code_label") + `</label>
                    <input type="text" id="code" name="code" required autofocus
                           class="form-input" placeholder="` + t("auth.totp.code_placeholder") + `"
                           autocomplete="one-time-code" inputmode="numeric" pattern="[0-9A-Fa-f]*">
                </div>
                <button type="submit" class="btn btn--primary btn--full">` + t("auth.totp.verify") + `</button>
            </form>
            <p class="login-hint">
                ` + t("auth.totp.hint") + `
            </p>
        </div>
    </div>
</body>
</html>`))
}
