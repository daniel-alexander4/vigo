// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

// session key constants
const (
	sessionKeyUser      = "user"
	sessionKeyCSRFToken = "csrf_token"
)

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

// GetCSRFToken returns the CSRF token from the session, generating one if needed.
func (sm *SessionManager) GetCSRFToken(ctx context.Context) string {
	token := sm.GetString(ctx, sessionKeyCSRFToken)
	if token == "" {
		token = sm.Token(ctx)
		sm.Put(ctx, sessionKeyCSRFToken, token)
	}
	return token
}
