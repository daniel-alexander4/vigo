// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoneProviderMiddleware(t *testing.T) {
	p := NoneProvider{}
	assert.Equal(t, "none", p.Method())

	// NoneProvider.Middleware should be a pure passthrough.
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := p.Middleware(inner)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(rec, req)

	assert.True(t, called, "inner handler should be called")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNoneProviderRegisterRoutes(t *testing.T) {
	p := NoneProvider{}
	mux := http.NewServeMux()
	// Should not panic.
	p.RegisterRoutes(mux)
}

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
		{"/", false},
		{"/thralls", false},
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
