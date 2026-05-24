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
// The token check the middleware then runs (the CLI dial carries the
// local-admin bearer) only proceeds if GetUser returns nil cleanly.
func TestLoadSessionReadOnly_NoCookie_GetUserDoesNotPanic(t *testing.T) {
	_, sm, _, _, _ := setupBasicProvider(t)

	req := httptest.NewRequest("GET", "/ws/scrier/anything", nil)
	ctx := sm.LoadSessionReadOnly(req)

	// Must not panic. Returns nil because the fresh scs session has no user.
	user := sm.GetUser(ctx)
	require.Nil(t, user)
}

// TestPutGetUserRoundTrips pins that the entire User snapshot survives the
// session round-trip. The session stores User as one gob value, so every
// field comes back. The earlier shape hand-projected named fields and
// silently dropped whichever the projection missed — SSHPublicKey (0.53.2
// browser-scrier "no SSH pubkey" bug) and then Permissions (0.66.24, which
// redirected viewer/compliance webusers off the dashboard because
// HasPermission saw an empty slice).
func TestPutGetUserRoundTrips(t *testing.T) {
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
		Role:         "viewer",
		Permissions:  ViewerPermissions,
		SSHPublicKey: "ssh-ed25519 AAAA-test-key-bytes operator@laptop",
		TOTPEnabled:  true,
	}
	sm.PutUser(ctx, original)

	got := sm.GetUser(ctx)
	require.NotNil(t, got)
	require.Equal(t, original, got, "the whole User snapshot must survive the session round-trip")

	// The concrete regression: a viewer's permissions must come back intact,
	// or the dashboard's fleet.read check redirects them to /compliance.
	require.True(t, got.HasPermission(PermFleetRead),
		"viewer must retain fleet.read after the session round-trip")
}

// TestDestroyUserSessions pins the invariant that DestroyUserSessions
// destroys every active session belonging to the given user ID and leaves
// other users' sessions intact. Backs the role-change/user-delete fix:
// without it a demoted user keeps their old role from the cached session
// for up to the idle timeout (15 min default).
func TestDestroyUserSessions(t *testing.T) {
	_, sm, _, _, _ := setupBasicProvider(t)
	ctx := context.Background()

	commit := func(u *User) string {
		t.Helper()
		sctx, err := sm.SessionManager.Load(ctx, "")
		require.NoError(t, err)
		sm.PutUser(sctx, u)
		token, _, err := sm.SessionManager.Commit(sctx)
		require.NoError(t, err)
		return token
	}
	loadUser := func(token string) *User {
		t.Helper()
		sctx, err := sm.SessionManager.Load(ctx, token)
		require.NoError(t, err)
		return sm.GetUser(sctx)
	}

	alice := &User{ID: "u-alice", Username: "alice", Role: "admin", Permissions: AllPermissions}
	bob := &User{ID: "u-bob", Username: "bob", Role: "viewer", Permissions: ViewerPermissions}

	// Two sessions for alice (browser + tab in another window), one for bob.
	aliceTok1 := commit(alice)
	aliceTok2 := commit(alice)
	bobTok := commit(bob)

	n, err := sm.DestroyUserSessions(ctx, alice.ID)
	require.NoError(t, err)
	require.Equal(t, 2, n, "both of alice's sessions must be destroyed")

	require.Nil(t, loadUser(aliceTok1), "alice's first session must be gone")
	require.Nil(t, loadUser(aliceTok2), "alice's second session must be gone")

	got := loadUser(bobTok)
	require.NotNil(t, got, "bob's session must survive — destroy is scoped to the named user")
	require.Equal(t, "bob", got.Username)

	// Empty userID is a no-op (defensive: never wipe the entire session table).
	n, err = sm.DestroyUserSessions(ctx, "")
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.NotNil(t, loadUser(bobTok), "empty userID must not affect any session")
}
