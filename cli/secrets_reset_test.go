// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/secrets"
)

func writeHexKey(t *testing.T, path string) {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(k)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestProveMasterKey: `secrets reset` must refuse anything but this vault's
// master key — a readable non-key file (the old check accepted
// --key-file /etc/hostname) and a well-formed key that decrypts nothing.
func TestProveMasterKey(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	realKey := filepath.Join(dir, "real.key")
	writeHexKey(t, realKey)
	p, err := secrets.NewLocalProvider(realKey, secretsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Set("vigo/db/dsn", "file:/x.db"); err != nil {
		t.Fatal(err)
	}

	if err := proveMasterKey(realKey, secretsDir); err != nil {
		t.Fatalf("real key refused: %v", err)
	}

	notAKey := filepath.Join(dir, "hostname")
	if err := os.WriteFile(notAKey, []byte("myhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := proveMasterKey(notAKey, secretsDir); err == nil || !strings.Contains(err.Error(), "is not a master key") {
		t.Fatalf("arbitrary readable file must be refused, got %v", err)
	}

	otherKey := filepath.Join(dir, "other.key")
	writeHexKey(t, otherKey)
	if err := proveMasterKey(otherKey, secretsDir); err == nil || !strings.Contains(err.Error(), "does not decrypt") {
		t.Fatalf("well-formed key that decrypts nothing must be refused, got %v", err)
	}

	if err := proveMasterKey(filepath.Join(dir, "absent"), secretsDir); err == nil {
		t.Fatal("missing key file must be refused")
	}

	// Empty vault: nothing to decrypt, so a well-formed key is all there is to check.
	emptyDir := filepath.Join(dir, "empty")
	if err := proveMasterKey(otherKey, emptyDir); err != nil {
		t.Fatalf("well-formed key against an empty vault: %v", err)
	}
}
