// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
		{"/bootstrap/agent", true},
		{"/metrics", true},
		{"/healthz", true},
		{"/login", true},
		{"/auth/callback", true},
		{"/auth/isowebauth/challenge", true},
		{"/static/css/style.css", true},
		{"/api/v1/bootstrap", true},
		{"/api/v1/agent/download/linux/arm64", true},
		{"/api/v1/agent/latest", true},
		{"/api/v1/bootstrap/register", true},
		// Public-by-construction (name↔pubkey / hostname↔pubkey bindings).
		// Agents fetch these with only an mTLS client cert, no token.
		{"/api/v1/fleet/pubkeys", true},
		{"/api/v1/swarm/puddle/names", true},
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
