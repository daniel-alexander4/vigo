// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package auth provides pluggable authentication for the Vigo web UI and
// REST API. Three methods are supported: "basic" (default, username/password
// with credentials in the secrets store), "oidc" (OpenID Connect SSO), and
// "isowebauth" (SSH key-based signing).
package auth

import (
	"context"
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
}

// publicPaths are exact URL paths that never require authentication.
var publicPaths = map[string]bool{
	"/bootstrap":          true,
	"/bootstrap/agent":    true,
	"/bootstrap/cert":     true,
	"/bootstrap/csr":      true,
	"/metrics":            true,
	"/healthz":            true,
	"/login":              true,
	"/totp":               true,
	"/setup":              true,
	"/api/v1/bootstrap":   true,
	"/api/v1/config/reload":  true,
	"/api/v1/secrets/rotated": true,
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

