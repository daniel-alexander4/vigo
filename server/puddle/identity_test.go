// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package puddle

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/hkdf"
)

// fakeDeriver mimics the slice of LocalProvider.DeriveKey we depend on,
// without pulling the secrets package (and its full master-key plumbing)
// into puddle's test surface.
type fakeDeriver struct {
	masterKey []byte
}

func (f *fakeDeriver) DeriveKey(purpose string, salt []byte, outLen int) ([]byte, error) {
	if outLen <= 0 {
		return nil, errors.New("outLen <= 0")
	}
	r := hkdf.New(sha256.New, f.masterKey, salt, []byte(purpose))
	out := make([]byte, outLen)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

func newDeriver(t *testing.T) *fakeDeriver {
	t.Helper()
	mk := make([]byte, 32)
	for i := range mk {
		mk[i] = byte(i + 1)
	}
	return &fakeDeriver{masterKey: mk}
}

func TestEnsureIdentity_FirstCall_GeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	id, err := EnsureIdentity(d, dir)
	if err != nil {
		t.Fatalf("EnsureIdentity: %v", err)
	}
	if len(id.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("pubkey size = %d, want %d", len(id.PublicKey), ed25519.PublicKeySize)
	}
	if !strings.HasPrefix(id.PublicKeyText, "ed25519:") {
		t.Fatalf("pubkey text %q missing ed25519: prefix", id.PublicKeyText)
	}

	// Sign-then-verify round-trip proves the SigningKey matches the PublicKey.
	msg := []byte("vigo puddle slice 1 round-trip")
	sig := ed25519.Sign(id.SigningKey, msg)
	if !ed25519.Verify(id.PublicKey, msg, sig) {
		t.Fatal("signature did not verify against returned pubkey")
	}

	// All three files present with the right modes.
	for path, wantMode := range map[string]os.FileMode{
		WrappedPath(dir): 0o600,
		SaltPath(dir):    0o600,
		PubkeyPath(dir):  0o644,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != wantMode {
			t.Errorf("%s mode = %v, want %v", path, got, wantMode)
		}
	}

	// Salt length matches contract.
	salt, _ := os.ReadFile(SaltPath(dir))
	if len(salt) != SaltLen {
		t.Errorf("salt is %d bytes, want %d", len(salt), SaltLen)
	}

	// Envelope versioned as 0x02 (HKDF-derived).
	env, _ := os.ReadFile(WrappedPath(dir))
	if env[0] != WrapVersion {
		t.Errorf("envelope version byte = 0x%02x, want 0x%02x", env[0], WrapVersion)
	}

	// Pubkey file content matches PublicKeyText (+ trailing newline).
	pubFile, _ := os.ReadFile(PubkeyPath(dir))
	if string(pubFile) != id.PublicKeyText+"\n" {
		t.Errorf("pubkey file content mismatch:\nfile: %q\nwant: %q\n", pubFile, id.PublicKeyText+"\n")
	}
}

func TestEnsureIdentity_SecondCall_ReturnsSameKey(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	first, err := EnsureIdentity(d, dir)
	if err != nil {
		t.Fatalf("first EnsureIdentity: %v", err)
	}
	second, err := EnsureIdentity(d, dir)
	if err != nil {
		t.Fatalf("second EnsureIdentity: %v", err)
	}
	if !bytes.Equal(first.PublicKey, second.PublicKey) {
		t.Fatal("second call returned a different pubkey — identity is not stable")
	}
	if !bytes.Equal(first.SigningKey, second.SigningKey) {
		t.Fatal("second call returned a different signing key — identity is not stable")
	}
}

func TestLoadIdentity_MissingWrappedFile(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	_, err := LoadIdentity(d, dir)
	if err == nil {
		t.Fatal("LoadIdentity on empty dir should fail")
	}
}

func TestEnsureIdentity_MasterKeyRotation_FailsUnwrap(t *testing.T) {
	dir := t.TempDir()
	d1 := newDeriver(t)

	if _, err := EnsureIdentity(d1, dir); err != nil {
		t.Fatalf("initial EnsureIdentity: %v", err)
	}

	// Operator rotates the master key. The wrapped envelope is now
	// undecryptable; we expect a clear failure, NOT a silent regeneration
	// (which would orphan the puddle's swarm membership).
	d2 := &fakeDeriver{masterKey: append([]byte("rotated-"), d1.masterKey...)}
	_, err := EnsureIdentity(d2, dir)
	if err == nil {
		t.Fatal("EnsureIdentity should fail after master-key rotation")
	}
	if !strings.Contains(err.Error(), "unwrap failed") {
		t.Errorf("error %q should mention 'unwrap failed' to point operators at the rotation issue", err.Error())
	}
}

func TestEnsureIdentity_SaltTampering_FailsUnwrap(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	if _, err := EnsureIdentity(d, dir); err != nil {
		t.Fatalf("initial EnsureIdentity: %v", err)
	}
	// Flip one bit in the salt → wrap key changes → unwrap fails.
	salt, _ := os.ReadFile(SaltPath(dir))
	salt[0] ^= 0x01
	if err := os.WriteFile(SaltPath(dir), salt, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureIdentity(d, dir)
	if err == nil {
		t.Fatal("EnsureIdentity should fail when salt is tampered")
	}
}

func TestEnsureIdentity_WrongVersionByte(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	if _, err := EnsureIdentity(d, dir); err != nil {
		t.Fatalf("initial: %v", err)
	}
	// Synthesize a v1 (Argon2id) envelope by flipping the version byte.
	env, _ := os.ReadFile(WrappedPath(dir))
	env[0] = 0x01
	if err := os.WriteFile(WrappedPath(dir), env, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureIdentity(d, dir)
	if err == nil {
		t.Fatal("EnsureIdentity should refuse an unknown version byte")
	}
	if !strings.Contains(err.Error(), "unknown version") {
		t.Errorf("error %q should mention 'unknown version'", err.Error())
	}
}

func TestEnsureIdentity_CreatesDirWith0700(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "puddle-fresh")
	d := newDeriver(t)

	if _, err := EnsureIdentity(d, dir); err != nil {
		t.Fatalf("EnsureIdentity: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %v, want 0o700", got)
	}
}

func TestEncodePubkey_FormatMatchesAgent(t *testing.T) {
	// Fixed pubkey bytes → fixed text; ensures Go and Rust agree on
	// the wire format (lockbox::crypt::encode_sign_pubkey).
	pk := make(ed25519.PublicKey, ed25519.PublicKeySize)
	for i := range pk {
		pk[i] = byte(i)
	}
	got := EncodePubkey(pk)
	want := "ed25519:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	if got != want {
		t.Errorf("EncodePubkey =\n  got  %q\n  want %q", got, want)
	}
}

func TestWrapUnwrap_RoundTrip(t *testing.T) {
	seed := make([]byte, seedLen)
	for i := range seed {
		seed[i] = byte(0xAA ^ i)
	}
	wk := make([]byte, wrapKeyLen)
	for i := range wk {
		wk[i] = byte(0x55 ^ i)
	}
	env, err := wrap(seed, wk)
	if err != nil {
		t.Fatal(err)
	}
	out, err := unwrap(env, wk)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(seed, out) {
		t.Errorf("round-trip mismatch")
	}
}
