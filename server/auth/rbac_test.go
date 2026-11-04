// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequireAdminBlocksViewer(t *testing.T) {
	mw := RequireAdmin()
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	handler := mw(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/tasks", nil)
	ctx := ContextWithUser(req.Context(), &User{ID: "1", Role: "viewer"})
	handler.ServeHTTP(rec, req.WithContext(ctx))

	assert.False(t, called)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRequireAdminAllowsAdmin(t *testing.T) {
	mw := RequireAdmin()
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	handler := mw(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/tasks", nil)
	ctx := ContextWithUser(req.Context(), &User{ID: "1", Role: "admin"})
	handler.ServeHTTP(rec, req.WithContext(ctx))

	assert.True(t, called)
}

func TestRequireAdminBlocksUnauthenticated(t *testing.T) {
	mw := RequireAdmin()
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	handler := mw(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/tasks", nil)
	handler.ServeHTTP(rec, req)

	assert.False(t, called)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRequireAuthBlocksUnauthenticated(t *testing.T) {
	mw := RequireAuth()
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	handler := mw(inner)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/auth/tokens", nil)
	handler.ServeHTTP(rec, req)

	assert.False(t, called)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
