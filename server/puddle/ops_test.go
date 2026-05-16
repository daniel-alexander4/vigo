// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package puddle

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"strings"
	"testing"
)

const founderPubkeyExample = "ed25519:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestEnsureOpsPuddle_FirstFounding(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	ops, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample)
	if err != nil {
		t.Fatalf("EnsureOpsPuddle: %v", err)
	}
	if ops.FriendlyName != "alexander4-ops" {
		t.Errorf("FriendlyName = %q", ops.FriendlyName)
	}
	if len(ops.Members) != 1 || ops.Members[0].Pubkey != founderPubkeyExample {
		t.Errorf("Members = %+v, expected single founder", ops.Members)
	}
	if ops.Members[0].JoinedAt.IsZero() {
		t.Error("JoinedAt should be set")
	}
	if !strings.HasPrefix(ops.PublicKeyText, "ed25519:") {
		t.Errorf("PublicKeyText %q missing prefix", ops.PublicKeyText)
	}

	// Sign-then-verify on the ops-puddle key proves the wrap/unwrap chain.
	msg := []byte("ops-puddle slice 2a round-trip")
	sig := ed25519.Sign(ops.SigningKey, msg)
	if !ed25519.Verify(ops.PublicKey, msg, sig) {
		t.Fatal("ops-puddle signature did not verify against its pubkey")
	}
}

func TestEnsureOpsPuddle_SecondCallLoadsSame(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	first, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := EnsureOpsPuddle(d, dir, "alexander4-ops", "")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !bytes.Equal(first.PublicKey, second.PublicKey) {
		t.Fatal("ops-puddle pubkey changed across loads — identity is not stable")
	}
	if !bytes.Equal(first.SigningKey, second.SigningKey) {
		t.Fatal("ops-puddle signing key changed across loads")
	}
}

func TestEnsureOpsPuddle_NameMismatchRefused(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	if _, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample); err != nil {
		t.Fatalf("initial: %v", err)
	}
	_, err := EnsureOpsPuddle(d, dir, "different-name", "")
	if err == nil {
		t.Fatal("renaming an existing ops-puddle should refuse")
	}
	if !strings.Contains(err.Error(), "friendly name mismatch") {
		t.Errorf("error %q should mention friendly name mismatch", err.Error())
	}
}

func TestEnsureOpsPuddle_FirstFoundingRequiresFounderPubkey(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	_, err := EnsureOpsPuddle(d, dir, "alexander4-ops", "")
	if err == nil {
		t.Fatal("first founding without founderPubkey should fail")
	}
}

func TestEnsureOpsPuddle_FounderPubkeyFormat(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	_, err := EnsureOpsPuddle(d, dir, "alexander4-ops", "raw-hex-not-prefixed")
	if err == nil {
		t.Fatal("non-ed25519: pubkey should fail")
	}
}

func TestEnsureOpsPuddle_DistinctWrapPurposeBindsToOpsHKDF(t *testing.T) {
	// Founding an ops-puddle and re-loading it must use OpsHKDFPurpose,
	// not HKDFPurpose. If the loader confused the two, unwrap would fail
	// because the derived wrap key would differ. The pass = the test
	// reaches loadOpsPuddle and succeeds.
	dir := t.TempDir()
	d := newDeriver(t)
	if _, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := LoadOpsPuddle(d, dir); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestEnsureOpsPuddle_FilesOnDisk(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	if _, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample); err != nil {
		t.Fatalf("init: %v", err)
	}

	wantModes := map[string]os.FileMode{
		WrappedPath(dir):       0o600,
		SaltPath(dir):          0o600,
		PubkeyPath(dir):        0o644,
		friendlyNamePath(dir):  0o600,
		membersPath(dir):       0o600,
	}
	for path, want := range wantModes {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %v, want %v", path, got, want)
		}
	}

	// Friendly name file is a single line.
	b, _ := os.ReadFile(friendlyNamePath(dir))
	if string(b) != "alexander4-ops\n" {
		t.Errorf("friendly name file = %q, want %q", b, "alexander4-ops\n")
	}
}

func TestValidateFriendlyName(t *testing.T) {
	// 64-char non-hex name to exercise the upper-length-bound path without
	// tripping the "looks like a project_id" guard.
	max := strings.Repeat("z", 64)
	good := []string{"alexander4-ops", "ops", "a", "ops.staging", "ops_v2", max}
	for _, n := range good {
		if err := ValidateFriendlyName(n); err != nil {
			t.Errorf("ValidateFriendlyName(%q) rejected: %v", n, err)
		}
	}
	bad := []string{
		"",                       // empty
		strings.Repeat("a", 65),  // too long
		".hidden",                // leading dot
		"has space",              // space
		"has/slash",              // slash
		"has:colon",              // colon
		strings.Repeat("a", 32) + strings.Repeat("b", 32), // 64 hex-like chars
	}
	for _, n := range bad {
		if err := ValidateFriendlyName(n); err == nil {
			t.Errorf("ValidateFriendlyName(%q) accepted, should fail", n)
		}
	}
}
