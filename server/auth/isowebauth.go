// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsowebauthProvider implements Provider using SSH key-based signing via
// the isowebauth desktop app (localhost:7890).
type IsowebauthProvider struct {
	sessions    *SessionManager
	userStore   *UserStore
	tokenStore  *TokenStore
	namespace   string
	auditWriter auditRecorder
}

// IsowebauthConfig holds the isowebauth configuration from server.yaml.
type IsowebauthConfig struct {
	Namespace string `yaml:"namespace"`
}

// NewIsowebauthProvider creates an isowebauth auth provider. It validates that
// ssh-keygen is available on the system (required for signature verification).
func NewIsowebauthProvider(cfg IsowebauthConfig, sessions *SessionManager, userStore *UserStore, tokenStore *TokenStore) (*IsowebauthProvider, error) {
	// Verify ssh-keygen is available.
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return nil, fmt.Errorf("isowebauth requires ssh-keygen: %w", err)
	}

	ns := cfg.Namespace
	if ns == "" {
		ns = "vigo"
	}

	return &IsowebauthProvider{
		sessions:   sessions,
		userStore:  userStore,
		tokenStore: tokenStore,
		namespace:  ns,
	}, nil
}

// SetAuditWriter sets the audit writer for recording auth events.
func (p *IsowebauthProvider) SetAuditWriter(w auditRecorder) { p.auditWriter = w }

func (p *IsowebauthProvider) Method() string { return "isowebauth" }

// Middleware checks for a valid session or API token. Same logic as OIDC.
func (p *IsowebauthProvider) Middleware(next http.Handler) http.Handler {
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

// RegisterRoutes mounts the isowebauth-specific routes.
func (p *IsowebauthProvider) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", p.handleLoginPage)
	mux.HandleFunc("GET /auth/isowebauth/challenge", p.handleChallenge)
	mux.HandleFunc("POST /auth/isowebauth/verify", p.handleVerify)
	mux.HandleFunc("POST /logout", p.handleLogout)
}

func (p *IsowebauthProvider) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// Serve the login page — the isowebauth JS flow is embedded in the template.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(isowebauthLoginHTML))
}

func (p *IsowebauthProvider) handleChallenge(w http.ResponseWriter, r *http.Request) {
	// Generate a random 32-byte challenge.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	challenge := hex.EncodeToString(b)

	// Store in session with the namespace.
	p.sessions.Put(r.Context(), "iwa_challenge", challenge)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"challenge": challenge,
		"namespace": p.namespace,
	})
}

func (p *IsowebauthProvider) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Challenge string `json:"challenge"`
		Signature string `json:"signature"`
		Username  string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Verify challenge matches the one we issued (replay protection).
	expected := p.sessions.PopString(r.Context(), "iwa_challenge")
	if expected == "" || req.Challenge != expected {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid or expired challenge"})
		return
	}

	// Look up user by username.
	user, err := p.userStore.GetByUsername(r.Context(), req.Username)
	if err != nil {
		slog.Error("user lookup failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", req.Username, "auth", "user not found", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "user not found"})
		return
	}
	if user.SSHPublicKey == "" {
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", req.Username, "auth", "no SSH key configured", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "no SSH key configured for user"})
		return
	}

	// Verify the signature using ssh-keygen.
	if err := verifySSHSignature(req.Username, user.SSHPublicKey, req.Challenge, req.Signature, p.namespace); err != nil {
		slog.Warn("isowebauth signature verification failed", "username", req.Username, "error", err)
		recordAudit(r.Context(), p.auditWriter, "auth.login_failed", req.Username, "auth", "signature verification failed", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "signature verification failed"})
		return
	}

	// Create session.
	if err := p.sessions.RenewToken(r.Context()); err != nil {
		slog.Error("session renewal failed", "error", err)
	}
	p.sessions.PutUser(r.Context(), user)

	recordAudit(r.Context(), p.auditWriter, "auth.login", req.Username, "auth", "isowebauth login", fmt.Sprintf(`{"ip":%q}`, r.RemoteAddr))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
}

func (p *IsowebauthProvider) handleLogout(w http.ResponseWriter, r *http.Request) {
	user := p.sessions.GetUser(r.Context())
	actor := ""
	if user != nil {
		actor = user.Username
	}
	_ = p.sessions.DestroyUser(r.Context())
	recordAudit(r.Context(), p.auditWriter, "auth.logout", actor, "auth", "logout", "")
	http.Redirect(w, r, "/login", http.StatusFound)
}

// verifySSHSignature verifies an SSH signature using ssh-keygen -Y verify.
// This shells out to ssh-keygen because Go's x/crypto/ssh doesn't support
// the OpenSSH signature format that isowebauth produces.
func verifySSHSignature(username, pubkey, challenge, signature, namespace string) error {
	tmpDir, err := os.MkdirTemp("", "vigo-iwa-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write allowed_signers file: "username <pubkey>"
	allowedSigners := filepath.Join(tmpDir, "allowed_signers")
	line := fmt.Sprintf("%s %s\n", username, strings.TrimSpace(pubkey))
	if err := os.WriteFile(allowedSigners, []byte(line), 0600); err != nil {
		return fmt.Errorf("writing allowed_signers: %w", err)
	}

	// Write signature file.
	sigFile := filepath.Join(tmpDir, "challenge.sig")
	if err := os.WriteFile(sigFile, []byte(signature), 0600); err != nil {
		return fmt.Errorf("writing signature: %w", err)
	}

	// Write challenge data.
	challengeFile := filepath.Join(tmpDir, "challenge")
	if err := os.WriteFile(challengeFile, []byte(challenge), 0600); err != nil {
		return fmt.Errorf("writing challenge: %w", err)
	}

	// Run ssh-keygen -Y verify.
	cmd := exec.Command("ssh-keygen", "-Y", "verify",
		"-f", allowedSigners,
		"-I", username,
		"-n", namespace,
		"-s", sigFile,
	)
	challengeData, err := os.Open(challengeFile)
	if err != nil {
		return fmt.Errorf("opening challenge file: %w", err)
	}
	defer challengeData.Close()
	cmd.Stdin = challengeData

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh-keygen verify: %s: %w", strings.TrimSpace(string(output)), err)
	}

	return nil
}

// isowebauthLoginHTML is a self-contained login page for the isowebauth flow.
const isowebauthLoginHTML = `<!DOCTYPE html>
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
            <h1 class="login-title">Vigo</h1>
            <p class="login-subtitle">Sign in with your SSH key</p>
            <form id="login-form" onsubmit="return handleLogin(event)">
                <div class="form-group">
                    <label for="username">Username</label>
                    <input type="text" id="username" name="username" required autofocus
                           class="form-input" placeholder="Enter your username">
                </div>
                <button type="submit" class="btn btn--primary btn--full" id="login-btn">
                    Sign In
                </button>
                <p class="login-error" id="login-error" style="display:none"></p>
                <p class="login-hint">
                    Requires the <a href="https://github.com/vigo/isowebauth" target="_blank">isowebauth</a> app running on your machine.
                </p>
            </form>
        </div>
    </div>
    <script>
    async function handleLogin(e) {
        e.preventDefault();
        const btn = document.getElementById('login-btn');
        const errEl = document.getElementById('login-error');
        const username = document.getElementById('username').value.trim();
        if (!username) return;

        btn.disabled = true;
        btn.textContent = 'Signing...';
        errEl.style.display = 'none';

        try {
            // 1. Get challenge from server.
            const challengeResp = await fetch('/auth/isowebauth/challenge');
            if (!challengeResp.ok) throw new Error('Failed to get challenge');
            const { challenge, namespace } = await challengeResp.json();

            // 2. Ask isowebauth app to sign.
            let signResp;
            try {
                signResp = await fetch('http://127.0.0.1:7890/sign', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ challenge, namespace }),
                });
            } catch (err) {
                throw new Error('Cannot connect to isowebauth app. Is it running?');
            }
            const signData = await signResp.json();
            if (!signData.ok) throw new Error(signData.error || 'Signing refused');

            // 3. Verify signature with server.
            const verifyResp = await fetch('/auth/isowebauth/verify', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ challenge, signature: signData.signature, username }),
            });
            const verifyData = await verifyResp.json();
            if (!verifyResp.ok) throw new Error(verifyData.error || 'Verification failed');

            // Success — redirect to dashboard.
            window.location.href = '/';
        } catch (err) {
            errEl.textContent = err.message;
            errEl.style.display = 'block';
            btn.disabled = false;
            btn.textContent = 'Sign In';
        }
    }
    </script>
</body>
</html>`
