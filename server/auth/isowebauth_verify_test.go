// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.alexander4.io/vigo/server/db"
)

// iwaVerify runs one challenge → verify round trip through the session
// middleware and returns the verify response.
func iwaVerify(t *testing.T, p *IsowebauthProvider, username, signature string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	h := p.sessions.LoadAndSave(mux)

	chRec := httptest.NewRecorder()
	h.ServeHTTP(chRec, httptest.NewRequest(http.MethodGet, "/auth/isowebauth/challenge", nil))
	require.Equal(t, http.StatusOK, chRec.Code)
	var ch struct{ Challenge string }
	require.NoError(t, json.Unmarshal(chRec.Body.Bytes(), &ch))

	body, _ := json.Marshal(map[string]string{"challenge": ch.Challenge, "signature": signature, "username": username})
	req := httptest.NewRequest(http.MethodPost, "/auth/isowebauth/verify", strings.NewReader(string(body)))
	for _, c := range chRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestIsowebauthVerify_UniformFailure: an unknown user, a user with no key
// on file, and a bad signature are indistinguishable to the caller — the
// endpoint is not a username oracle.
func TestIsowebauthVerify_UniformFailure(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	p, _, _ := setupIsowebauthProvider(t)
	p.namespace = "vigo"
	_, err := p.userStore.Create(context.Background(), db.InsertUserParams{
		Username:     "bob",
		Role:         "viewer",
		SshPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBTnhQDY+EoHzuH6lTBahbzZVGcRcGEjhBBg0CLVK+cd bob@vigo",
	})
	require.NoError(t, err)

	var bodies []string
	for _, user := range []string{"nobody", "alice", "bob"} {
		rec := iwaVerify(t, p, user, "-----BEGIN SSH SIGNATURE-----\nAAAA\n-----END SSH SIGNATURE-----\n")
		assert.Equal(t, http.StatusUnauthorized, rec.Code, user)
		bodies = append(bodies, strings.TrimSpace(rec.Body.String()))
	}
	assert.Equal(t, bodies[0], bodies[1], "unknown user vs no-key user must look the same")
	assert.Equal(t, bodies[0], bodies[2], "unknown user vs bad signature must look the same")
	assert.Contains(t, bodies[0], verifyFailedMsg)
}

// TestIsowebauthVerify_RateLimited: verify shares basic login's per-IP
// budget; past it the caller gets 429 before any lookup or ssh-keygen.
func TestIsowebauthVerify_RateLimited(t *testing.T) {
	p, _, _ := setupIsowebauthProvider(t)
	p.authLimiter = newAuthLimiter(1, 1)

	first := iwaVerify(t, p, "nobody", "x")
	assert.Equal(t, http.StatusUnauthorized, first.Code)
	second := iwaVerify(t, p, "nobody", "x")
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
}
