// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLoadSessionReadOnly_NoCookie_GetUserDoesNotPanic is the regression case
// for the 0.52.1 Scrier WebSocket EOF bug: a CLI dialing `wss://.../ws/scrier/*`
// has no session cookie. The auth middleware's session lookup must return a
// context that's safe to call GetUser on — otherwise scs panics with
// "no session data in context" and the WebSocket handshake dies before any
// HTTP response, surfacing to the CLI as EOF.
//
// The fallthrough that the middleware relies on (TryPeerAuth grants loopback
// admin for the CLI case) only runs if GetUser returns nil cleanly.
func TestLoadSessionReadOnly_NoCookie_GetUserDoesNotPanic(t *testing.T) {
	_, sm, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest("GET", "/ws/scrier/anything", nil)
	ctx := sm.LoadSessionReadOnly(req)

	// Must not panic. Returns nil because the fresh scs session has no user.
	user := sm.GetUser(ctx)
	require.Nil(t, user)
}

// TestPutGetUser_SSHPublicKeyRoundTrips is the regression case for the 0.53.2
// browser-scrier "no SSH pubkey available" bug: the session-cached User struct
// must carry SSHPublicKey alongside the other five fields, otherwise browser
// scrier sessions read an empty pubkey at setupSession even when the DB has
// the user's key stored.
func TestPutGetUser_SSHPublicKeyRoundTrips(t *testing.T) {
	_, sm, _, _, _ := setupBasicProvider(t)

	// Load a fresh scs session into a context so Put/Get have somewhere to
	// write. Empty token mints an empty session — same path LoadAndSave
	// takes on a request with no cookie.
	ctx, err := sm.SessionManager.Load(context.Background(), "")
	require.NoError(t, err)

	original := &User{
		ID:           "u-1",
		Username:     "dan",
		DisplayName:  "Dan Alexander",
		Email:        "dan@example.com",
		Role:         "admin",
		SSHPublicKey: "ssh-ed25519 AAAA-test-key-bytes operator@laptop",
	}
	sm.PutUser(ctx, original)

	got := sm.GetUser(ctx)
	require.NotNil(t, got)
	require.Equal(t, original.ID, got.ID)
	require.Equal(t, original.Username, got.Username)
	require.Equal(t, original.DisplayName, got.DisplayName)
	require.Equal(t, original.Email, got.Email)
	require.Equal(t, original.Role, got.Role)
	require.Equal(t, original.SSHPublicKey, got.SSHPublicKey,
		"SSHPublicKey must survive the session round-trip — browser scrier reads this")
}
