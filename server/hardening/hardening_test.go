// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package hardening

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSignAndVerifyBinary(t *testing.T) {
	dir := t.TempDir()

	// Create a fake binary.
	binaryPath := filepath.Join(dir, "vigosrv")
	binaryContent := []byte("this is a test binary with some content 1234567890")
	if err := os.WriteFile(binaryPath, binaryContent, 0755); err != nil {
		t.Fatal(err)
	}

	// Generate a key.
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "signing.key")
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(seed)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Sign.
	sigPath := filepath.Join(dir, "vigosrv.sig")
	if err := SignBinary(binaryPath, keyPath, sigPath); err != nil {
		t.Fatalf("SignBinary: %v", err)
	}

	// Verify signature exists.
	if _, err := os.Stat(sigPath); err != nil {
		t.Fatalf("signature file not created: %v", err)
	}

	// Read and manually verify.
	privKey := ed25519.NewKeyFromSeed(seed)
	pubKey := privKey.Public().(ed25519.PublicKey)

	h := sha256.Sum256(binaryContent)
	sigData, _ := os.ReadFile(sigPath)

	// Parse sig file.
	var sigHex, pubHex string
	fmt.Sscanf(string(sigData), "%s\n%s", &sigHex, &pubHex)

	sig, _ := hex.DecodeString(sigHex)
	filePub, _ := hex.DecodeString(pubHex)

	if !ed25519.Verify(pubKey, h[:], sig) {
		t.Fatal("signature should verify")
	}

	if hex.EncodeToString(filePub) != hex.EncodeToString(pubKey) {
		t.Fatal("public key mismatch in sig file")
	}
}

func TestSignBinaryTamperedFails(t *testing.T) {
	dir := t.TempDir()

	// Create and sign a binary.
	binaryPath := filepath.Join(dir, "vigosrv")
	if err := os.WriteFile(binaryPath, []byte("original content"), 0755); err != nil {
		t.Fatal(err)
	}

	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "signing.key")
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(seed)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	sigPath := filepath.Join(dir, "vigosrv.sig")
	if err := SignBinary(binaryPath, keyPath, sigPath); err != nil {
		t.Fatal(err)
	}

	// Tamper with the binary.
	if err := os.WriteFile(binaryPath, []byte("tampered content!"), 0755); err != nil {
		t.Fatal(err)
	}

	// Manual verification should fail.
	sigData, _ := os.ReadFile(sigPath)
	var sigHex, pubHex string
	fmt.Sscanf(string(sigData), "%s\n%s", &sigHex, &pubHex)
	sig, _ := hex.DecodeString(sigHex)
	pub, _ := hex.DecodeString(pubHex)

	tamperedContent, _ := os.ReadFile(binaryPath)
	h := sha256.Sum256(tamperedContent)

	if ed25519.Verify(ed25519.PublicKey(pub), h[:], sig) {
		t.Fatal("tampered binary should NOT verify")
	}
}

func TestDetectDebugger(t *testing.T) {
	// Should not panic on any platform; on a clean test run no debugger
	// is attached, so no findings are returned.
	findings := DetectDebugger()
	for _, f := range findings {
		if f.Kind != "debugger" {
			t.Errorf("DetectDebugger returned wrong kind %q", f.Kind)
		}
		if f.Summary == "" {
			t.Error("DetectDebugger finding has empty summary")
		}
	}
}

func TestDetectInjection(t *testing.T) {
	// Should not panic on any platform. May return findings on systems
	// with LD_LIBRARY_PATH set (containers, dev wrappers) — accept that
	// and validate the shape rather than the absence.
	findings := DetectInjection()
	for _, f := range findings {
		switch f.Kind {
		case "ld_preload", "ld_library_path", "suspicious_map":
			// expected
		default:
			t.Errorf("DetectInjection returned unexpected kind %q", f.Kind)
		}
		if f.Summary == "" {
			t.Error("DetectInjection finding has empty summary")
		}
		if len(f.Detail) == 0 {
			t.Errorf("DetectInjection finding %q has empty Detail map", f.Kind)
		}
	}
}

func TestVerifyBinarySignatureNoSigFile(t *testing.T) {
	// Unsigned build (no baked pubkey): a missing .sig is optional → nil.
	// serverSigningPubKey is "" in the test binary.
	err := VerifyBinarySignature()
	if err != nil {
		t.Fatalf("expected nil with no sig file and no baked pubkey, got: %v", err)
	}
}

func TestVerifyBinarySignatureMissingSigFailClosed(t *testing.T) {
	// Signed build (baked pubkey present) with a missing .sig must fail closed:
	// a deployed-without-signature binary is a tamper/incomplete-deploy signal.
	// The test binary has no adjacent .sig, so setting the baked pubkey is
	// enough to exercise the branch.
	orig := serverSigningPubKey
	serverSigningPubKey = "deadbeef" // any non-empty value → "signed build"
	defer func() { serverSigningPubKey = orig }()

	err := VerifyBinarySignature()
	if err == nil {
		t.Fatal("expected fail-closed error for missing .sig in a signed build, got nil")
	}
}

// TestPubkeyFromKey covers the agent-pubkey extraction path the Makefile
// uses to derive VIGO_AGENT_SIGNING_PUBKEY from SIGN_KEY (ADR-031).
// Pubkey returned MUST match the pubkey of the keypair that signs the
// .sig files, or every fleet agent update would fail at runtime.
func TestPubkeyFromKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signing.key")
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(seed)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := PubkeyFromKey(keyPath)
	if err != nil {
		t.Fatalf("PubkeyFromKey: %v", err)
	}

	// Derive expected pubkey the same way SignBinary does internally —
	// asserting equality between the two paths is the whole point.
	want := hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	if got != want {
		t.Fatalf("pubkey mismatch — derived from SIGN_KEY differs from in-process derive\n  got:  %s\n  want: %s", got, want)
	}
}

func TestPubkeyFromKeyMissingFile(t *testing.T) {
	_, err := PubkeyFromKey(filepath.Join(t.TempDir(), "nope.key"))
	if err == nil {
		t.Fatal("expected error for missing key file")
	}
}
