// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package compliancedocs

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/secrets"
)

// A doc ciphertext written by a pre-v1 server (raw master key, no AAD) is
// re-sealed as v1 bound to its doc id when the store opens, and then reads.
func TestMigrateV0Docs(t *testing.T) {
	root := t.TempDir()
	secretsDir := filepath.Join(root, "secrets")
	os.MkdirAll(secretsDir, 0o700)
	key := make([]byte, 32)
	rand.Read(key)
	os.WriteFile(filepath.Join(secretsDir, secrets.MasterKeyFile), []byte(hex.EncodeToString(key)), 0o600)
	lp, err := secrets.NewLocalProvider("", secretsDir)
	if err != nil {
		t.Fatal(err)
	}

	docsDir := filepath.Join(root, "docs")
	os.MkdirAll(docsDir, 0o700)
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	nonce := make([]byte, g.NonceSize())
	rand.Read(nonce)
	const id = "3f2c9a1e-0000-4000-8000-000000000001"
	os.WriteFile(filepath.Join(docsDir, id), g.Seal(nonce, nonce, []byte("policy text"), nil), 0o600)

	s := &Store{docsDir: docsDir, crypto: lp}
	s.migrateV0()

	data, _ := os.ReadFile(filepath.Join(docsDir, id))
	pt, err := lp.Decrypt(secrets.PurposeComplianceDoc, []byte(id), data)
	if err != nil || string(pt) != "policy text" {
		t.Fatalf("migrated doc: %q, %v", pt, err)
	}
	// Bound to its id: it does not open as another doc.
	if _, err := lp.Decrypt(secrets.PurposeComplianceDoc, []byte(strings.Replace(id, "1", "2", 1)), data); err == nil {
		t.Fatal("migrated doc opened under another doc id")
	}
}
