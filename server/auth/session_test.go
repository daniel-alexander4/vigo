// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
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
