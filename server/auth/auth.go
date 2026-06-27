// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package auth provides pluggable authentication for the Vigo web UI and
// REST API. Three methods are supported: "basic" (default, username/password
// with credentials in the secrets store), "oidc" (OpenID Connect SSO), and
// "isowebauth" (SSH key-based signing).
package auth

import (
	"context"
	"errors"
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
	// Poolq (ADR-029) read endpoints — messages are fleet-readable
	// (confidentiality is not a goal; pair with lockbox for confidential
	// payloads), so reads are public-by-construction like curator's
	// blob/resolve. mTLS is the transport guard. Block/unblock admin
	// moderation is registered separately with its own admin() wrapper.
	"/api/v1/swarm/poolq/",
}

// publicPaths are exact URL paths that never require authentication.
var publicPaths = map[string]bool{
	"/bootstrap":        true,
	"/bootstrap/agent":  true,
	"/bootstrap/cert":   true,
	"/bootstrap/csr":    true,
	"/metrics":          true,
	"/healthz":          true,
	"/login":            true,
	"/totp":             true,
	"/api/v1/bootstrap": true,
	// /api/v1/secrets/rotated is INTENTIONALLY NOT public. The route's own
	// admin() wrapper (server/api/config.go) handles auth. Listing it here
	// bypassed the auth middleware entirely (no user injected into context),
	// then the per-route admin check rejected the missing-user request with
	// 403 "admin role required" even for vigocli running on the server host
	// — exact same shape as the /api/v1/config/reload comment below.
	// The local-admin token handles the server-host case: vigocli sends it
	// as a bearer, the admin user gets injected, and the gate clears.
	//
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
	// Spanner bolt admission (ADR-026) — a joining bolt POSTs its invite
	// bundle here during `vigocli spanner bolt join`. It's authenticated by
	// the founder-signed, single-use bundle itself (validated in handleAdmit),
	// NOT by an operator session — the joiner has no credentials on this bolt
	// yet (that's the point of the invite). Registered bare (no per-route
	// wrapper), same as fleet/pubkeys above, so the no-user request just runs
	// the handler. Without this, the default `basic` auth 401s the cross-bolt
	// /admit call and `spanner bolt join` can never complete.
	"/api/v1/spanner/bolt/admit": true,
	// Spanner config-driven hands-off peering (ADR-026) — a joining bolt POSTs
	// its self-signed identity here on boot for the founder to auto-admit it.
	// Bare like /admit above: the joiner carries no operator session;
	// authenticated by the federation's spanner_id + the joiner's Ed25519
	// self-signature + the founder's source-address allowlist (handlePeerAdmit).
	"/api/v1/spanner/bolt/peer-admit": true,
	// Spanner Tier-2 gossip pulls (ADR-026) — a PEER bolt's gossiper GETs
	// these on the Tier-2 cadence with no operator session and verifies the
	// founder/bolt SIGNATURE on the payload (server/spanner MergeSnapshot),
	// not the caller. Registered bare in spanner_admission.go, same as
	// /admit above. Without these, every inter-bolt pull 401s and per-bolt
	// observability never converges. (/api/v1/spanner/roster, by contrast,
	// is an operator read and stays PermFleetRead-gated.)
	"/api/v1/spanner/snapshot":       true,
	"/api/v1/spanner/fleet-snapshot": true,
	// /api/v1/config/reload is INTENTIONALLY NOT public. The route's own
	// RequirePermission(PermConfigPublish) wrapper handles auth — adding
	// it here bypassed the auth middleware entirely (no user injected
	// into context), then the per-route permission check rejected the
	// missing-user request with 401 even for vigocli running on the
	// server host. The local-admin token handles the server-host case:
	// vigocli sends it as a bearer and the admin user gets injected.
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

// LocalAdminUserID is the synthetic principal ID of the local-admin token.
// It is deliberately not a real users-table row; handlers that must treat
// the bootstrap principal specially (e.g. requiring an explicit token owner)
// compare against this rather than re-hardcoding the literal.
const LocalAdminUserID = "localhost"

// ErrReservedUsername is returned by [UserStore.Create] when the requested
// username is a reserved synthetic principal identifier (see
// [IsReservedPrincipal]).
var ErrReservedUsername = errors.New("username is reserved")

// IsReservedPrincipal reports whether s is an identifier reserved for a
// synthetic principal — today only the local-admin bootstrap user
// ([LocalAdminUserID]). No users-table row may take it as id or username, or
// it would be conflated with that principal: the token-owner orphan guard
// keys on id == LocalAdminUserID, so a DB row with that id would let an
// otherwise-orphaned token resolve. Add future reserved ids here.
func IsReservedPrincipal(s string) bool {
	return s == LocalAdminUserID
}

// localAdminUser is the synthetic admin identity resolved for a request
// bearing the local-admin token (see localadmin.go). It is not a row in
// the users table — it is the runtime identity of "root on the server
// host", the only caller that can read the 0600 token file. It replaces
// the former source-IP "peer auth", which trusted any loopback or
// Docker-bridge caller and so handed admin to non-root local users.
var localAdminUser = &User{
	ID:          LocalAdminUserID,
	Username:    LocalAdminUserID,
	Role:        "admin",
	Permissions: AllPermissions,
}
