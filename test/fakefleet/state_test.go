// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestStateRoundTrip confirms the identity manifest survives a
// save/load cycle byte-for-byte — the property a resumed tick relies on
// to re-sign as the same envoys without re-enrolling.
func TestStateRoundTrip(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	identity := json.RawMessage(`{"os":"linux","cloud_provider":"aws"}`)
	in := &fleetState{
		Version:   stateVersion,
		Count:     1,
		StacksDir: "/srv/vigo/stacks",
		Hosts: []hostState{{
			hostPlan: hostPlan{
				Hostname:     "web-prod-use1a-001",
				Configcrate:  "web",
				IdentityJSON: identity,
				Outcome:      "failed",
			},
			EnvoyID:       "abc-123",
			SigningKey:    []byte(priv),
			CertPEM:       []byte("CERT"),
			KeyPEM:        []byte("KEY"),
			CACertPEM:     []byte("CA"),
			InventoryHash: "deadbeef",
		}},
	}

	path := filepath.Join(t.TempDir(), "state.json")
	if err := saveState(path, in); err != nil {
		t.Fatal(err)
	}

	// 0600 — it holds private keys.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("state file perm = %o, want 600", perm)
	}

	out, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Hosts) != 1 {
		t.Fatalf("got %d hosts", len(out.Hosts))
	}
	h := out.Hosts[0]
	if h.EnvoyID != "abc-123" || h.Hostname != in.Hosts[0].Hostname || h.Outcome != "failed" {
		t.Errorf("scalar fields did not round-trip: %+v", h)
	}
	if !bytes.Equal(h.SigningKey, []byte(priv)) {
		t.Error("signing key did not round-trip")
	}
	if !ed25519.PrivateKey(h.SigningKey).Equal(priv) {
		t.Error("reconstructed ed25519 key is not equal to the original")
	}
	if !bytes.Equal(h.IdentityJSON, identity) {
		t.Errorf("identity did not round-trip: %s", h.IdentityJSON)
	}
	if h.InventoryHash != "deadbeef" {
		t.Errorf("inventory hash did not round-trip: %s", h.InventoryHash)
	}
}

// TestLoadStateMissingIsNotExist confirms a missing manifest is reported
// as os.IsNotExist, which `tick` relies on to exit cleanly.
func TestLoadStateMissingIsNotExist(t *testing.T) {
	_, err := loadState(filepath.Join(t.TempDir(), "absent.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("want IsNotExist, got %v", err)
	}
}
