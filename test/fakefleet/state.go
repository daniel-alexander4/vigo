// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// hostState is one synthetic envoy's full record: its generation intent
// (hostPlan) plus the enrollment material a tick needs to act as it
// without re-enrolling. The signing key is the only secret here that
// exists nowhere else — the server holds only the matching pubkey.
type hostState struct {
	hostPlan
	EnvoyID    string `json:"envoy_id"`
	SigningKey []byte `json:"signing_key"` // ed25519 private key (seed||pub, 64 bytes)
	CertPEM    []byte `json:"cert_pem"`
	KeyPEM     []byte `json:"key_pem"`
	CACertPEM  []byte `json:"ca_cert_pem"`
	// InventoryHash is the stable ADR-033 inventory hash this envoy sends on
	// every check-in (sha256 of identity ∪ shared bulk). Constant across ticks
	// so the server's vitals-only cache lookup keeps hitting. Computed at
	// create time.
	InventoryHash string `json:"inventory_hash"`
}

// fleetState is the on-disk manifest. It is the harness's own record of
// the identities it created — not a copy of any server-owned data — so a
// restarted tick (after process death, host reboot, or server restart)
// resumes the same envoys instead of enrolling duplicates.
type fleetState struct {
	Version   int    `json:"version"`
	Count     int    `json:"count"`
	StacksDir string `json:"stacks_dir"`
	// CleanupDirs are the exact directories create wrote (the configcrates
	// dir + each fleet-dir cloud root). backout removes precisely these, so
	// it never touches the operator's real config in the same fleet dir.
	CleanupDirs []string `json:"cleanup_dirs"`
	// InventoryBulk and Vitals are the shared ADR-033 split-traits payloads —
	// one copy each for the whole fleet. Every envoy's inventory is its
	// per-host identity merged with this bulk; vitals ship verbatim every tick.
	// Stored once here so the manifest stays ~25 MB at 60k hosts, not ~13 GB.
	InventoryBulk json.RawMessage `json:"inventory_bulk"`
	Vitals        json.RawMessage `json:"vitals"`
	Hosts         []hostState     `json:"hosts"`
}

const stateVersion = 1

// signingKey returns the ed25519 private key for signing.
func (h *hostState) signingKey() ed25519.PrivateKey { return ed25519.PrivateKey(h.SigningKey) }

// saveState writes the manifest atomically at 0600 — it holds every
// envoy's private key.
func saveState(path string, st *fleetState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadState reads the manifest. A missing file is reported distinctly so
// `tick` can exit cleanly (nothing to resume) rather than error.
func loadState(path string) (*fleetState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st fleetState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &st, nil
}
