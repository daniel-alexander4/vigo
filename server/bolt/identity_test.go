// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package bolt

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

	"git.alexander4.io/vigo/server/puddle"
	"golang.org/x/crypto/hkdf"
)

// fakeDeriver mirrors puddle/identity_test.go's deriver — same shape,
// independent test surface so bolt package tests don't depend on
// puddle's internal test helpers.
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

func TestEnsureIdentity_FirstCallGeneratesAndPersists(t *testing.T) {
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
	for _, p := range []string{WrappedPath(dir), SaltPath(dir), PubkeyPath(dir)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s on disk: %v", p, err)
		}
	}
	// Sign-then-verify proves the wrap/unwrap chain end-to-end.
	msg := []byte("bolt phase 1 round-trip")
	sig := ed25519.Sign(id.SigningKey, msg)
	if !ed25519.Verify(id.PublicKey, msg, sig) {
		t.Fatal("bolt signature did not verify against its pubkey")
	}
}

func TestEnsureIdentity_SecondCallLoadsSame(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	first, err := EnsureIdentity(d, dir)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := EnsureIdentity(d, dir)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !bytes.Equal(first.PublicKey, second.PublicKey) {
		t.Fatal("bolt pubkey changed across loads — identity is not stable")
	}
	if !bytes.Equal(first.SigningKey, second.SigningKey) {
		t.Fatal("bolt signing key changed across loads")
	}
}

func TestLoadIdentity_FailsWithoutWrap(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	if _, err := LoadIdentity(d, dir); err == nil {
		t.Fatal("LoadIdentity should error when wrap file is absent")
	}
}

func TestDistinctHKDFPurpose_FromPuddle(t *testing.T) {
	// Both packages must use distinct purposes — a bolt wrap
	// generated under puddle.HKDFPurpose would unwrap correctly via
	// puddle.LoadIdentity, defeating the role boundary. Confirm the
	// HKDF purpose strings differ at the constant level.
	if HKDFPurpose == puddle.HKDFPurpose {
		t.Fatal("bolt.HKDFPurpose must differ from puddle.HKDFPurpose")
	}
	if HKDFPurpose == puddle.OpsHKDFPurpose {
		t.Fatal("bolt.HKDFPurpose must differ from puddle.OpsHKDFPurpose")
	}
}

func TestWrapPath_DotSubdirConvention(t *testing.T) {
	// Catch accidental rename of the canonical filenames.
	dir := "/srv/vigo/bolt"
	if got, want := WrappedPath(dir), filepath.Join(dir, "identity.wrapped"); got != want {
		t.Errorf("WrappedPath = %q, want %q", got, want)
	}
	if got, want := SaltPath(dir), filepath.Join(dir, ".salt"); got != want {
		t.Errorf("SaltPath = %q, want %q", got, want)
	}
	if got, want := PubkeyPath(dir), filepath.Join(dir, "identity.pub"); got != want {
		t.Errorf("PubkeyPath = %q, want %q", got, want)
	}
}
