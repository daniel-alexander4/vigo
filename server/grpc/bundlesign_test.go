// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
)

func TestBundleSigner_SignAndVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer := NewBundleSigner(priv)

	bundle := &pb.PolicyBundle{
		PolicyVersion:  42,
		MatchedPattern: "*.web",
		Environment:    "production",
		Roles:          []string{"webserver"},
		Configcrates: []*pb.PolicyConfigcrate{
			{Name: "nginx", ContentHash: "abc123"},
		},
	}

	if err := signer.Sign(bundle, 86400); err != nil {
		t.Fatalf("Sign() error: %v", err)
	}

	if len(bundle.Signature) == 0 {
		t.Fatal("expected non-empty signature")
	}
	if bundle.ServerPubkeyId == "" {
		t.Fatal("expected non-empty server_pubkey_id")
	}
	if bundle.MaxAgeSeconds != 86400 {
		t.Fatalf("expected max_age_seconds=86400, got %d", bundle.MaxAgeSeconds)
	}

	if !VerifyBundle(bundle, pub) {
		t.Fatal("VerifyBundle() returned false for valid signature")
	}
}

func TestBundleSigner_RejectTampered(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer := NewBundleSigner(priv)

	bundle := &pb.PolicyBundle{
		PolicyVersion:  1,
		MatchedPattern: "*.db",
	}

	if err := signer.Sign(bundle, 0); err != nil {
		t.Fatal(err)
	}

	// Tamper with the bundle after signing.
	bundle.PolicyVersion = 999

	if VerifyBundle(bundle, pub) {
		t.Fatal("VerifyBundle() should reject tampered bundle")
	}
}

func TestBundleSigner_KeyID(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer := NewBundleSigner(priv)

	keyID := signer.KeyID()
	if len(keyID) != 16 { // 8 bytes hex-encoded = 16 chars
		t.Fatalf("expected 16-char key ID, got %d chars: %q", len(keyID), keyID)
	}

	// Same key should produce the same ID.
	signer2 := NewBundleSigner(priv)
	if signer2.KeyID() != keyID {
		t.Fatal("same key should produce same key ID")
	}
}
