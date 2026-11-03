// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package spanner

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPullRequestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubHex := hex.EncodeToString(pub)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/spanner/snapshot", nil)
	SignPullRequest(req, priv, pubHex)

	got, err := VerifyPullRequest(req, PullAuthWindow)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != pubHex {
		t.Errorf("authenticated pubkey = %q, want %q", got, pubHex)
	}
}

func TestPullRequestVerifyRejects(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubHex := hex.EncodeToString(pub)

	t.Run("missing headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/spanner/snapshot", nil)
		if _, err := VerifyPullRequest(req, PullAuthWindow); err == nil {
			t.Error("accepted a request carrying no signature")
		}
	})

	t.Run("path binding", func(t *testing.T) {
		// A proof minted for /snapshot must not verify on /fleet-snapshot.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/spanner/snapshot", nil)
		SignPullRequest(req, priv, pubHex)
		req.URL.Path = "/api/v1/spanner/fleet-snapshot"
		if _, err := VerifyPullRequest(req, PullAuthWindow); err == nil {
			t.Error("accepted a proof replayed to a different path")
		}
	})

	t.Run("expired timestamp", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/spanner/snapshot", nil)
		SignPullRequest(req, priv, pubHex)
		if _, err := VerifyPullRequest(req, -1*time.Second); err == nil {
			t.Error("accepted a timestamp outside the window")
		}
	})

	t.Run("pubkey mismatch", func(t *testing.T) {
		// Signed by priv, but the header claims a different pubkey.
		other, _, _ := ed25519.GenerateKey(rand.Reader)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/spanner/snapshot", nil)
		SignPullRequest(req, priv, pubHex)
		req.Header.Set(pullHeaderPubkey, hex.EncodeToString(other))
		if _, err := VerifyPullRequest(req, PullAuthWindow); err == nil {
			t.Error("accepted a signature that does not match the claimed pubkey")
		}
	})

	t.Run("no key is a no-op", func(t *testing.T) {
		// A gossiper without a bolt identity signs nothing rather than
		// panicking; the resulting request is unsigned and rejected.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/spanner/snapshot", nil)
		SignPullRequest(req, nil, pubHex)
		if req.Header.Get(pullHeaderSig) != "" {
			t.Error("a nil key must not attach a signature header")
		}
		if _, err := VerifyPullRequest(req, PullAuthWindow); err == nil {
			t.Error("accepted an unsigned request")
		}
	})
}
