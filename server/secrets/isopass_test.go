// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package secrets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockIsopassServer creates a test HTTP server that mimics the isopass API.
func mockIsopassServer(t *testing.T) *httptest.Server {
	t.Helper()

	var tokenCallCount atomic.Int32

	mux := http.NewServeMux()

	// Token exchange endpoint.
	mux.HandleFunc("POST /api/auth/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCallCount.Add(1)
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-bearer-token" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		resp := map[string]any{
			"token":      "short-lived-token-123",
			"expires_at": time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// Token renewal endpoint.
	mux.HandleFunc("POST /api/auth/token/renew", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer short-lived-token-123" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		resp := map[string]any{
			"token":      "short-lived-token-123",
			"expires_at": time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// Search endpoint.
	mux.HandleFunc("POST /api/sa/search", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer short-lived-token-123" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var body struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)

		secrets := map[string][]map[string]any{
			"hobnob/db/dsn": {
				{"id": "secret-1", "name": "hobnob/db/dsn", "value": "/srv/hobnob/db/hobnob.db", "version": 2},
			},
			"hobnob/tls/cert": {
				{"id": "secret-2", "name": "hobnob/tls/cert", "value": "-----BEGIN CERTIFICATE-----\nMIIB...", "version": 1},
			},
		}

		results, ok := secrets[body.Query]
		if !ok {
			results = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)
	})

	return httptest.NewServer(mux)
}

func TestIsopassGet(t *testing.T) {
	srv := mockIsopassServer(t)
	defer srv.Close()

	p, err := NewIsopassProvider(srv.URL, "test-bearer-token", false)
	require.NoError(t, err)
	defer p.Close()

	val, err := p.Get(context.Background(), "hobnob/db/dsn")
	require.NoError(t, err)
	assert.Equal(t, "/srv/hobnob/db/hobnob.db", val)
}

func TestIsopassGetNotFound(t *testing.T) {
	srv := mockIsopassServer(t)
	defer srv.Close()

	p, err := NewIsopassProvider(srv.URL, "test-bearer-token", false)
	require.NoError(t, err)
	defer p.Close()

	_, err = p.Get(context.Background(), "nonexistent/secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestIsopassGetBytes(t *testing.T) {
	srv := mockIsopassServer(t)
	defer srv.Close()

	p, err := NewIsopassProvider(srv.URL, "test-bearer-token", false)
	require.NoError(t, err)
	defer p.Close()

	val, err := p.GetBytes(context.Background(), "hobnob/tls/cert")
	require.NoError(t, err)
	assert.Equal(t, []byte("-----BEGIN CERTIFICATE-----\nMIIB..."), val)
}

func TestIsopassGetVersion(t *testing.T) {
	srv := mockIsopassServer(t)
	defer srv.Close()

	p, err := NewIsopassProvider(srv.URL, "test-bearer-token", false)
	require.NoError(t, err)
	defer p.Close()

	ver, err := p.GetVersion(context.Background(), "hobnob/db/dsn")
	require.NoError(t, err)
	assert.Equal(t, 2, ver)
}

func TestIsopassTokenExchange(t *testing.T) {
	srv := mockIsopassServer(t)
	defer srv.Close()

	p, err := NewIsopassProvider(srv.URL, "test-bearer-token", false)
	require.NoError(t, err)
	defer p.Close()

	// First call should trigger token exchange.
	_, err = p.Get(context.Background(), "hobnob/db/dsn")
	require.NoError(t, err)

	// Second call should reuse token (no re-exchange).
	_, err = p.Get(context.Background(), "hobnob/db/dsn")
	require.NoError(t, err)
}

func TestIsopassTokenRenewalOnExpiry(t *testing.T) {
	srv := mockIsopassServer(t)
	defer srv.Close()

	p, err := NewIsopassProvider(srv.URL, "test-bearer-token", false)
	require.NoError(t, err)
	defer p.Close()

	// Force the token to be expired.
	p.mu.Lock()
	p.token = "short-lived-token-123"
	p.tokenExpiry = time.Now().Add(-1 * time.Minute)
	p.mu.Unlock()

	// Call should trigger a new token exchange.
	val, err := p.Get(context.Background(), "hobnob/db/dsn")
	require.NoError(t, err)
	assert.Equal(t, "/srv/hobnob/db/hobnob.db", val)
}

func TestIsopassAuthFailure(t *testing.T) {
	srv := mockIsopassServer(t)
	defer srv.Close()

	p, err := NewIsopassProvider(srv.URL, "wrong-bearer-token", false)
	require.NoError(t, err)
	defer p.Close()

	_, err = p.Get(context.Background(), "hobnob/db/dsn")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestIsopassUnreachable(t *testing.T) {
	p, err := NewIsopassProvider("http://127.0.0.1:1", "token", false)
	require.NoError(t, err)
	defer p.Close()

	_, err = p.Get(context.Background(), "hobnob/db/dsn")
	require.Error(t, err)
}
