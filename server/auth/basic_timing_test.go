// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBasicLogin_UnknownUserPaysBcryptCost: an unknown user, or one with no
// password on file, used to fail before any bcrypt ran — faster than a wrong
// password, so response time enumerated usernames. Each must run exactly one
// bcrypt comparison (against the dummy hash) and still fail.
func TestBasicLogin_UnknownUserPaysBcryptCost(t *testing.T) {
	p, sm, _, _, _ := setupBasicProvider(t) // seeds "alice"; NullProvider → no password on file
	var calls int
	orig := passwordVerifier
	t.Cleanup(func() { passwordVerifier = orig })
	passwordVerifier = func(hash, plaintext string) bool {
		calls++
		return true // even a "matching" dummy compare must not log anyone in
	}
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	h := sm.LoadAndSave(mux)

	for _, user := range []string{"nobody", "alice"} {
		calls = 0
		form := url.Values{"username": {user}, "password": {"pw"}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, 1, calls, "%s: bcrypt comparisons run", user)
		assert.NotEqual(t, http.StatusSeeOther, rec.Code, "%s: login must fail", user)
		assert.Empty(t, rec.Header().Get("Location"), "%s: no redirect into the app", user)
	}
}
