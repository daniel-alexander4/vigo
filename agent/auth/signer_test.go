package auth_test

import (
	"crypto/ed25519"
	"testing"

	"github.com/isogov/isogov/agent/auth"
)

func TestSignPayload(t *testing.T) {
	_, priv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}

	payload := []byte("test payload data")
	timestamp := "2026-03-06T00:00:00Z"

	sig := auth.SignPayload(payload, timestamp, priv)
	if len(sig) == 0 {
		t.Fatal("SignPayload() returned empty signature")
	}
	if len(sig) != ed25519.SignatureSize {
		t.Errorf("signature length = %d, want %d", len(sig), ed25519.SignatureSize)
	}
}

func TestVerifyPayload(t *testing.T) {
	pub, priv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}

	payload := []byte("test payload data")
	timestamp := "2026-03-06T00:00:00Z"

	sig := auth.SignPayload(payload, timestamp, priv)

	if !auth.VerifyPayload(payload, timestamp, sig, pub) {
		t.Error("VerifyPayload() returned false for valid signature")
	}
}

func TestVerifyPayloadTampered(t *testing.T) {
	pub, priv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}

	payload := []byte("original payload")
	timestamp := "2026-03-06T00:00:00Z"

	sig := auth.SignPayload(payload, timestamp, priv)

	tamperedPayload := []byte("tampered payload")
	if auth.VerifyPayload(tamperedPayload, timestamp, sig, pub) {
		t.Error("VerifyPayload() returned true for tampered payload")
	}
}

func TestVerifyPayloadWrongKey(t *testing.T) {
	_, priv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}

	// Generate a different keypair
	wrongPub, _, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}

	payload := []byte("test payload data")
	timestamp := "2026-03-06T00:00:00Z"

	sig := auth.SignPayload(payload, timestamp, priv)

	if auth.VerifyPayload(payload, timestamp, sig, wrongPub) {
		t.Error("VerifyPayload() returned true with wrong public key")
	}
}

func TestSignatureIncludesTimestamp(t *testing.T) {
	pub, priv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}

	payload := []byte("test payload data")
	timestamp1 := "2026-03-06T00:00:00Z"
	timestamp2 := "2026-03-06T00:05:00Z"

	sig := auth.SignPayload(payload, timestamp1, priv)

	// Same payload but different timestamp should fail verification
	if auth.VerifyPayload(payload, timestamp2, sig, pub) {
		t.Error("VerifyPayload() returned true with different timestamp - timestamp not included in signed data")
	}

	// Original timestamp should still verify
	if !auth.VerifyPayload(payload, timestamp1, sig, pub) {
		t.Error("VerifyPayload() returned false for correct timestamp")
	}
}
