// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package auth provides pluggable authentication for the Hobnob web UI and
// REST API. Four methods are supported: "basic" (default, username/password
// with credentials in the secrets store), "none" (fully open), "oidc"
// (OpenID Connect SSO), and "isowebauth" (SSH key-based signing).
package auth

import (
	"context"
	"net/http"
	"strings"
)

// Provider is the interface that authentication methods implement.
type Provider interface {
	// Method returns the authentication method name ("none", "oidc", "isowebauth").
	Method() string

	// Middleware wraps an http.Handler with authentication checks.
	// For "none", this is a no-op passthrough. For others, it checks
	// sessions/tokens and redirects or returns 401 as appropriate.
	Middleware(next http.Handler) http.Handler

	// RegisterRoutes mounts auth-specific routes (e.g. /login, /logout,
	// /auth/callback) on the given mux. Called before middleware wrapping.
	RegisterRoutes(mux *http.ServeMux)
}

// User represents an authenticated user.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	SSHPublicKey string `json:"ssh_public_key,omitempty"`
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
// Returns nil if no user is present (unauthenticated request or "none" mode).
func UserFromContext(ctx context.Context) *User {
	u, _ := ctx.Value(userContextKey).(*User)
	return u
}

// publicPrefixes are URL path prefixes that never require authentication.
var publicPrefixes = []string{
	"/static/",
	"/auth/",
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
	"/setup":              true,
	"/api/v1/bootstrap":   true,
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

// NoneProvider implements Provider with zero overhead — all requests pass through.
type NoneProvider struct{}

func (NoneProvider) Method() string                          { return "none" }
func (NoneProvider) Middleware(next http.Handler) http.Handler { return next }
func (NoneProvider) RegisterRoutes(_ *http.ServeMux)         {}
