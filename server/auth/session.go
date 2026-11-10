// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"encoding/gob"
	"net/http"
	"time"

	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"
)

// SessionManager wraps the SCS session manager and provides typed helpers
// for storing/retrieving user data in the session.
type SessionManager struct {
	*scs.SessionManager
}

// DefaultIdleTimeout is the default session idle timeout (HIPAA 164.312(a)(2)(iii)).
const DefaultIdleTimeout = 15 * time.Minute

// NewSessionManager creates a session manager backed by SQLite (sharing the
// existing *sql.DB). Sessions expire after 24 hours with idle timeout.
// Pass 0 for idleTimeout to use the default (15 minutes).
func NewSessionManager(sqlDB *sql.DB, idleTimeout time.Duration) *SessionManager {
	if idleTimeout == 0 {
		idleTimeout = DefaultIdleTimeout
	}
	sm := scs.New()
	sm.Store = sqlite3store.New(sqlDB)
	sm.Lifetime = 24 * time.Hour
	sm.IdleTimeout = idleTimeout
	sm.Cookie.Name = "vigo_session"
	sm.Cookie.HttpOnly = true
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Secure = true // server always runs behind TLS
	return &SessionManager{SessionManager: sm}
}

// sessionKeyUser is the session key holding the authenticated user. CSRF is
// handled by CrossOriginGuard, not by a session-stored token.
const sessionKeyUser = "user"

// The SCS session map is gob-encoded; the User snapshot stored under
// sessionKeyUser needs its concrete type registered before the first
// encode/decode.
func init() {
	gob.Register(User{})
}

// PutUser stores the authenticated user snapshot in the session.
func (sm *SessionManager) PutUser(ctx context.Context, u *User) {
	sm.Put(ctx, sessionKeyUser, *u)
}

// GetUser retrieves the authenticated user snapshot from the session, or nil
// if none is stored. The snapshot is taken at login: rotated fields (role,
// permissions, email, ssh_public_key) require the operator to re-login for
// changes to take effect.
func (sm *SessionManager) GetUser(ctx context.Context) *User {
	u, ok := sm.Get(ctx, sessionKeyUser).(User)
	if !ok {
		return nil
	}
	return &u
}

// DestroyUser removes the user from the session and destroys it.
func (sm *SessionManager) DestroyUser(ctx context.Context) error {
	return sm.Destroy(ctx)
}

// DestroyUserSessions destroys every active session whose stored user
// snapshot matches the given user ID. Returns the number of sessions
// destroyed and the first error encountered (if any); on per-session
// Destroy error the walk continues so a single bad row doesn't strand
// the rest. Called by user-update handlers when a role changes or a
// user is deleted, so the new role takes effect immediately rather
// than after the 15-minute idle (or 24-hour absolute) timeout.
//
// SCS doesn't index sessions by user — the user is stored inside the
// gob-encoded value — so we walk every active session. At Vigo's scale
// (tens of web users, a handful of concurrent sessions each) the O(n)
// walk is cheap; role changes are rare. If session count grows past
// the point where this matters, switch to a user_id sessions-table
// column rather than gob-walking on every demotion.
func (sm *SessionManager) DestroyUserSessions(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, nil
	}
	destroyed := 0
	var firstErr error
	iterErr := sm.SessionManager.Iterate(ctx, func(sctx context.Context) error {
		u, ok := sm.Get(sctx, sessionKeyUser).(User)
		if !ok {
			return nil
		}
		if u.ID != userID {
			return nil
		}
		if err := sm.Destroy(sctx); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		destroyed++
		return nil
	})
	if iterErr != nil && firstErr == nil {
		firstErr = iterErr
	}
	return destroyed, firstErr
}

// LoadSessionReadOnly loads session data into the context from the request's
// session cookie WITHOUT wrapping the ResponseWriter. Used for WebSocket
// upgrade requests where the ResponseWriter must implement http.Hijacker.
func (sm *SessionManager) LoadSessionReadOnly(r *http.Request) context.Context {
	cookie, err := r.Cookie(sm.Cookie.Name)
	token := ""
	if err == nil {
		token = cookie.Value
	}
	// Empty token matches LoadAndSave's no-cookie semantics — scs mints a
	// fresh empty session bound to the context. Without this, downstream
	// callers of GetUser/GetString panic on a context that never saw scs.
	ctx, err := sm.SessionManager.Load(r.Context(), token)
	if err != nil {
		return r.Context()
	}
	return ctx
}
