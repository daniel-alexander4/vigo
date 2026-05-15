// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package auth provides pluggable authentication for the Vigo web UI and
// REST API. Three methods are supported: "basic" (default, username/password
// with credentials in the secrets store), "oidc" (OpenID Connect SSO), and
// "isowebauth" (SSH key-based signing).
package auth

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// Provider is the interface that authentication methods implement.
type Provider interface {
	// Method returns the authentication method name ("basic", "oidc", "isowebauth").
	Method() string

	// Middleware wraps an http.Handler with authentication checks.
	// Checks sessions/tokens and redirects or returns 401 as appropriate.
	Middleware(next http.Handler) http.Handler

	// RegisterRoutes mounts auth-specific routes (e.g. /login, /logout,
	// /auth/callback) on the given mux. Called before middleware wrapping.
	RegisterRoutes(mux *http.ServeMux)
}

// User represents an authenticated user.
type User struct {
	ID           string   `json:"id"`
	Username     string   `json:"username"`
	DisplayName  string   `json:"display_name"`
	Email        string   `json:"email"`
	Role         string   `json:"role"`
	Permissions  []string `json:"permissions,omitempty"`
	SSHPublicKey string   `json:"ssh_public_key,omitempty"`
	TOTPEnabled  bool     `json:"totp_enabled,omitempty"`
}

// IsAdmin returns true if the user has the admin role.
func (u *User) IsAdmin() bool {
	return u != nil && u.Role == "admin"
}

// IsCompliance returns true if the user has the compliance role —
// GRC staff and compliance-document uploaders. Used for nav gating
// where role-label parity with admin/viewer is the cleanest check;
// authorization itself runs through HasPermission.
func (u *User) IsCompliance() bool {
	return u != nil && u.Role == "compliance"
}

type contextKey int

const userContextKey contextKey = iota

// ContextWithUser returns a new context with the given user stored in it.
func ContextWithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}

// UserFromContext extracts the authenticated user from the context.
// Returns nil if no user is present (unauthenticated request).
func UserFromContext(ctx context.Context) *User {
	u, _ := ctx.Value(userContextKey).(*User)
	return u
}

// publicPrefixes are URL path prefixes that never require authentication.
var publicPrefixes = []string{
	"/static/",
	"/auth/",
	"/api/v1/agent/",
	"/api/v1/bootstrap/",
	"/api/v1/swarm/filecast/blob/",
	// Curator (ADR-024) blob relay — artifacts are fleet-readable (the
	// publish side is gated, not the read side), so the relay is
	// public-by-construction like the name map below; mTLS is the
	// transport guard. The handler scopes it to curator artifact blobs
	// only, so it isn't a back door to relay filecast/lockbox/gitback bytes.
	"/api/v1/swarm/curator/blob/",
}

// publicPaths are exact URL paths that never require authentication.
var publicPaths = map[string]bool{
	"/bootstrap":              true,
	"/bootstrap/agent":        true,
	"/bootstrap/cert":         true,
	"/bootstrap/csr":          true,
	"/metrics":                true,
	"/healthz":                true,
	"/login":                  true,
	"/totp":                   true,
	"/setup":                  true,
	"/api/v1/bootstrap":       true,
	"/api/v1/secrets/rotated": true,
	// fleet/pubkeys and swarm/puddle/names are public-by-construction:
	// hostname↔pubkey and name↔pubkey bindings, nothing secret (the
	// pubkeys ride every multicast GITBACK: line; the names are the
	// gitback://<name>/<repo> URL form). Agents fetch them on a timer
	// with only an mTLS client cert — no session token — so they have to
	// be in the allowlist or the agent never bootstraps its peer-pubkey
	// cache / friendly-name map. Both handlers are registered bare (no
	// per-route permission wrapper), so unlike /api/v1/config/reload
	// below this is safe: the no-user request just runs the handler.
	"/api/v1/fleet/pubkeys":      true,
	"/api/v1/swarm/puddle/names": true,
	// Curator (ADR-024) resolve — `<name>:<tag>` → blob coordinates +
	// the signed catalog entry; fleet-readable (same rationale as the
	// `blob/` prefix above). Registered bare (no per-route wrapper), so
	// the no-user request just runs the handler.
	"/api/v1/swarm/curator/resolve": true,
	// /api/v1/config/reload is INTENTIONALLY NOT public. The route's own
	// RequirePermission(PermConfigPublish) wrapper handles auth — adding
	// it here bypassed the auth middleware entirely (no user injected
	// into context), then the per-route permission check rejected the
	// missing-user request with 401 even for vigocli running on the
	// server host. TryPeerAuth handles the localhost case correctly:
	// loopback admin gets injected and clears the permission gate.
}

// IsPublicPath returns true if the given URL path should be accessible
// without authentication (bootstrap, metrics, static assets, login, etc.).
func IsPublicPath(path string) bool {
	if publicPaths[path] {
		return true
	}
	for _, prefix := range publicPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// isAPIPath returns true if the path is a REST API endpoint (not a UI page).
func isAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/")
}

// isLocalRequest returns true if the request originates from the server host.
// Checks for loopback (127.0.0.1, ::1) and Docker bridge networks (172.16-31.x.x).
// This grants implicit admin access to CLI tools on the server host — if you
// have shell access, you already have full access to config, secrets, and the
// database (same model as PostgreSQL peer auth).
func isLocalRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	// Docker bridge: 172.16.0.0/12 — host requests arrive from the gateway IP
	// (e.g., 172.17.0.1) when the server runs in a container.
	_, dockerNet, _ := net.ParseCIDR("172.16.0.0/12")
	return dockerNet.Contains(ip)
}

// loopbackAdmin is the synthetic admin user injected for localhost API requests.
var loopbackAdmin = &User{
	ID:          "localhost",
	Username:    "localhost",
	Role:        "admin",
	Permissions: AllPermissions,
}

// TryPeerAuth is the shared peer-auth fallback called by every auth
// provider. Returns the synthetic loopback admin when (and only when):
//   - the request targets a /api/v1/* path OR a /ws/* path, AND
//   - the request originates from loopback or the Docker bridge,
//
// otherwise returns nil. Providers call this AFTER their token and
// session checks — a real user's cookie or token must always take
// precedence. See basic_test.go:77 for the regression that pins the
// ordering invariant.
//
// The /ws/* prefix is included so the CLI's WebSocket dial in
// `vigocli scrier ssh` works the same way its `/api/v1/scrier/*`
// REST calls already do — both are CLI-on-server-host traffic.
// Browser WS dials carry a session cookie and authenticate before
// peer-auth runs, so they're unaffected.
//
// Caller is responsible for injecting the returned user into the
// request context (ContextWithUser) and serving the downstream
// handler — this function doesn't write to the response.
func TryPeerAuth(r *http.Request) *User {
	if !peerAuthEligiblePath(r.URL.Path) || !isLocalRequest(r) {
		return nil
	}
	slog.Info("peer-auth granted loopback admin",
		"path", r.URL.Path,
		"remote", r.RemoteAddr,
	)
	return loopbackAdmin
}

// peerAuthEligiblePath returns true for the URL prefixes that peer-auth
// is allowed to grant on: the REST API (/api/v1/*) and the WebSocket
// endpoints (/ws/*). Other paths (login pages, marketing pages, static
// assets) are deliberately excluded — peer-auth fallthrough to those
// would short-circuit the browser-side /login flow.
func peerAuthEligiblePath(path string) bool {
	return isAPIPath(path) || strings.HasPrefix(path, "/ws/")
}
