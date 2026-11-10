// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/fleet"
)

func TestFleetPubkeys_ListsActiveEnvoys(t *testing.T) {
	fi := fleet.NewFleetIndex()

	// Three envoys, third one revoked.
	pkA, _, _ := ed25519.GenerateKey(rand.Reader)
	pkB, _, _ := ed25519.GenerateKey(rand.Reader)
	pkC, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now().UTC()
	fi.AddEnvoy(fleet.EnvoyState{ID: "id-a", Hostname: "alpha", EnrolledAt: now, LastSeen: now}, pkA)
	fi.AddEnvoy(fleet.EnvoyState{ID: "id-b", Hostname: "bravo", EnrolledAt: now, LastSeen: now}, pkB)
	fi.AddEnvoy(fleet.EnvoyState{ID: "id-c", Hostname: "charlie", EnrolledAt: now, LastSeen: now, Revoked: true}, pkC)

	handler := NewFleetPubkeysHandler(fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/fleet/pubkeys", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Pubkeys map[string]string `json:"pubkeys"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Pubkeys) != 2 {
		t.Fatalf("expected 2 pubkeys (revoked envoy excluded), got %d: %v", len(resp.Pubkeys), resp.Pubkeys)
	}
	if got, want := resp.Pubkeys["alpha"], hex.EncodeToString(pkA); got != want {
		t.Errorf("alpha pubkey = %q, want %q", got, want)
	}
	if got, want := resp.Pubkeys["bravo"], hex.EncodeToString(pkB); got != want {
		t.Errorf("bravo pubkey = %q, want %q", got, want)
	}
	if _, ok := resp.Pubkeys["charlie"]; ok {
		t.Errorf("charlie (revoked) should be excluded; got %q", resp.Pubkeys["charlie"])
	}
}

func TestFleetPubkeys_NilFleetIndexReturnsEmpty(t *testing.T) {
	handler := NewFleetPubkeysHandler(nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/fleet/pubkeys", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Pubkeys map[string]string `json:"pubkeys"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Pubkeys) != 0 {
		t.Errorf("expected empty pubkeys map, got %v", resp.Pubkeys)
	}
}

func TestFleetPubkeys_EnvoyWithoutPubkeySkipped(t *testing.T) {
	fi := fleet.NewFleetIndex()
	now := time.Now().UTC()
	// Envoy added with nil pubkey — covers a partially-bootstrapped state
	// where the pubkey hasn't been recorded yet.
	fi.AddEnvoy(fleet.EnvoyState{ID: "id-x", Hostname: "x", EnrolledAt: now, LastSeen: now}, nil)

	handler := NewFleetPubkeysHandler(fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/fleet/pubkeys", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var resp struct {
		Pubkeys map[string]string `json:"pubkeys"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Pubkeys) != 0 {
		t.Errorf("expected envoy with nil pubkey to be skipped; got %v", resp.Pubkeys)
	}
}
