// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// sealV0 produces a pre-v1 envelope: nonce || ct under the raw master key.
func sealV0(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := cipher.NewGCM(block)
	nonce := make([]byte, g.NonceSize())
	rand.Read(nonce)
	return g.Seal(nonce, nonce, plaintext, nil)
}

// v0Dir builds a secrets dir as a pre-v1 server left it: a hex master key
// and v0 secret files.
func v0Dir(t *testing.T, secrets map[string]string) (dir string, key []byte) {
	t.Helper()
	dir = t.TempDir()
	key = make([]byte, 32)
	rand.Read(key)
	if err := os.WriteFile(filepath.Join(dir, MasterKeyFile), []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range secrets {
		p := filepath.Join(dir, filepath.FromSlash(k))
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, sealV0(t, key, []byte(v)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, key
}

// Starting from v0 files, opening the provider migrates them in place and
// every value survives.
func TestLocalProviderMigratesV0Secrets(t *testing.T) {
	dir, _ := v0Dir(t, map[string]string{"vigo/db/dsn": "postgres://x", "smtp/password": "hunter2"})

	p, err := NewLocalProvider("", dir)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"vigo/db/dsn": "postgres://x", "smtp/password": "hunter2"} {
		got, err := p.Get(context.Background(), k)
		if err != nil || got != want {
			t.Fatalf("Get(%q) = %q, %v; want %q", k, got, err, want)
		}
		raw, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(k)))
		if !bytes.HasPrefix(raw, []byte(envelopeMagic)) {
			t.Fatalf("%s was not re-sealed as v1 on disk", k)
		}
	}
	// The master key file is untouched.
	if raw, _ := os.ReadFile(filepath.Join(dir, MasterKeyFile)); bytes.HasPrefix(raw, []byte(envelopeMagic)) {
		t.Fatal("master key file was rewritten")
	}
	// A second open is a no-op and still reads.
	p2, err := NewLocalProvider("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := p2.Get(context.Background(), "smtp/password"); got != "hunter2" {
		t.Fatalf("after re-open Get = %q", got)
	}
}

// Outside the migration a v0 envelope is refused, never decrypted.
func TestDecryptRefusesV0(t *testing.T) {
	dir, key := v0Dir(t, nil)
	p, err := NewLocalProvider("", dir)
	if err != nil {
		t.Fatal(err)
	}
	v0 := sealV0(t, key, []byte("doc"))
	if _, err := p.Decrypt(PurposeComplianceDoc, []byte("id"), v0); !errors.Is(err, ErrLegacyEnvelope) {
		t.Fatalf("Decrypt(v0) err = %v, want ErrLegacyEnvelope", err)
	}
}

// The raw master key no longer keys the secret cipher, and a ciphertext is
// bound to its path and purpose.
func TestEnvelopeSeparation(t *testing.T) {
	dir, key := v0Dir(t, nil)
	p, err := NewLocalProvider("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Set("a/one", "v1"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "a", "one"))

	// Not openable with the raw master key (no direct use of the master key).
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	body := raw[len(envelopeMagic):]
	if _, err := g.Open(nil, body[:g.NonceSize()], body[g.NonceSize():], envelopeAAD(purposeSecret, []byte("a/one"))); err == nil {
		t.Fatal("v1 secret opens under the raw master key")
	}

	// Copied to another path, it does not open.
	if err := os.WriteFile(filepath.Join(dir, "a", "two"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(context.Background(), "a/two"); err == nil {
		t.Fatal("a secret moved to another path still decrypted")
	}

	// Another purpose cannot open it.
	if _, err := p.Decrypt(PurposeComplianceDoc, []byte("a/one"), raw); err == nil {
		t.Fatal("secret ciphertext opened under the compliance-doc purpose")
	}
}
