// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPublicPath(t *testing.T) {
	tests := []struct {
		path   string
		public bool
	}{
		{"/bootstrap", true},
		// Dead public entries (no route serves them) were removed: a
		// handler later registered there must not become public silently.
		{"/bootstrap/agent", false},
		{"/bootstrap/cert", false},
		{"/bootstrap/csr", false},
		{"/api/v1/bootstrap", false},
		{"/api/v1/swarm/filecast/blob/abc", false},
		{"/metrics", true},
		{"/login", true},
		{"/auth/callback", true},
		{"/auth/isowebauth/challenge", true},
		{"/static/css/style.css", true},
		// Poolq: the GET read routes are public, the admin moderation
		// routes are not (they need the middleware to resolve the caller).
		{"/api/v1/swarm/poolq/topics", true},
		{"/api/v1/swarm/poolq/topic/abc/head", true},
		{"/api/v1/swarm/poolq/topic/abc/range", true},
		{"/api/v1/swarm/poolq/topic/abc/log", true},
		{"/api/v1/swarm/poolq/block/abc", false},
		{"/api/v1/swarm/poolq/unblock/abc", false},
		{"/api/v1/swarm/curator/blob/abc", true},
		// Fetched by the bootstrap script with no session (Apache-2.0 section 4).
		{"/healthz", true},
		{"/license/LICENSE", true},
		{"/license/NOTICE", true},
		{"/license/other", false},
		{"/api/v1/agent/download/linux/arm64", true},
		{"/api/v1/agent/latest", true},
		{"/api/v1/bootstrap/register", true},
		// Public-by-construction (name↔pubkey / hostname↔pubkey bindings).
		// Agents fetch these with only an mTLS client cert, no token.
		{"/api/v1/fleet/pubkeys", true},
		{"/api/v1/swarm/puddle/names", true},
		// Spanner bolt admission — authenticated by the invite bundle itself
		// (handleAdmit), not an operator session; a joining bolt has no creds
		// on this bolt yet. Must be public or `spanner bolt join` 401s.
		{"/api/v1/spanner/bolt/admit", true},
		// Spanner Tier-2 gossip pulls — signature-authenticated payloads the
		// peer gossiper verifies; no operator session. Public, or observability
		// never converges. But /roster is an operator read and stays gated.
		{"/api/v1/spanner/snapshot", true},
		{"/api/v1/spanner/fleet-snapshot", true},
		{"/api/v1/spanner/roster", false},
		// ...but the exact-match granularity holds: the authed gitback
		// fleet view stays gated.
		{"/api/v1/swarm/gitback/fleet", false},
		// /api/v1/config/reload must NOT be public — the route guards it
		// with RequirePermission(PermConfigPublish). Marking the path
		// public bypassed the auth middleware (no user in context), then
		// the per-route permission check rejected the user-less request
		// with 401, breaking vigocli config publish from the server host.
		// Localhost callers get peer-auth → loopback admin → permission
		// passes; remote callers without a token correctly get 401.
		{"/api/v1/config/reload", false},
		// /api/v1/secrets/rotated must NOT be public — same shape as
		// /api/v1/config/reload above. The route's admin() wrapper handles
		// auth; listing it public used to bypass user injection and return
		// 403 "admin role required" to vigocli secrets set on the server
		// host, silently breaking rotation propagation (0.53.8 regression).
		{"/api/v1/secrets/rotated", false},
		{"/", false},
		{"/envoys", false},
		{"/api/v1/users", false},
		{"/api/v1/tasks", false},
		{"/admin/users", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.public, IsPublicPath(tt.path))
		})
	}
}

func TestContextUser(t *testing.T) {
	ctx := context.Background()

	// No user in empty context.
	assert.Nil(t, UserFromContext(ctx))

	// Store and retrieve user.
	u := &User{ID: "u1", Username: "alice", Role: "admin"}
	ctx = ContextWithUser(ctx, u)
	got := UserFromContext(ctx)
	require.NotNil(t, got)
	assert.Equal(t, "u1", got.ID)
	assert.Equal(t, "alice", got.Username)
	assert.True(t, got.IsAdmin())
}

func TestUserIsAdmin(t *testing.T) {
	assert.False(t, (*User)(nil).IsAdmin())
	assert.False(t, (&User{Role: "viewer"}).IsAdmin())
	assert.True(t, (&User{Role: "admin"}).IsAdmin())
}
