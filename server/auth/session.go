// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"database/sql"
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
	sm.Cookie.Secure = false // set to true in production behind TLS
	return &SessionManager{SessionManager: sm}
}

// session key constants
const (
	sessionKeyUserID      = "user_id"
	sessionKeyUsername    = "username"
	sessionKeyDisplayName = "display_name"
	sessionKeyEmail       = "email"
	sessionKeyRole        = "role"
	sessionKeyCSRFToken   = "csrf_token"
)

// PutUser stores the user in the session.
func (sm *SessionManager) PutUser(ctx context.Context, u *User) {
	sm.Put(ctx, sessionKeyUserID, u.ID)
	sm.Put(ctx, sessionKeyUsername, u.Username)
	sm.Put(ctx, sessionKeyDisplayName, u.DisplayName)
	sm.Put(ctx, sessionKeyEmail, u.Email)
	sm.Put(ctx, sessionKeyRole, u.Role)
}

// GetUser retrieves the user from the session. Returns nil if no user is stored.
func (sm *SessionManager) GetUser(ctx context.Context) *User {
	id := sm.GetString(ctx, sessionKeyUserID)
	if id == "" {
		return nil
	}
	return &User{
		ID:          id,
		Username:    sm.GetString(ctx, sessionKeyUsername),
		DisplayName: sm.GetString(ctx, sessionKeyDisplayName),
		Email:       sm.GetString(ctx, sessionKeyEmail),
		Role:        sm.GetString(ctx, sessionKeyRole),
	}
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
	if err != nil || cookie.Value == "" {
		return r.Context()
	}
	ctx, err := sm.SessionManager.Load(r.Context(), cookie.Value)
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
