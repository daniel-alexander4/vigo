// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"crypto/ed25519"
	"testing"
	"time"
)

// genKey returns a fresh Ed25519 keypair for tests.
func genKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func TestInviteBundleRoundTrip(t *testing.T) {
	pub, priv := genKey(t)

	bundle, err := MintInviteBundle(priv, "test-spanner", "https://srv1:8443", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if bundle.Token == "" {
		t.Fatal("minted bundle has empty token")
	}
	if string(bundle.FounderPubkey) != string(pub) {
		t.Fatal("minted bundle founder pubkey mismatch")
	}

	str, err := MarshalBundle(bundle)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	parsed, err := ParseBundle(str)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Token != bundle.Token {
		t.Errorf("round-trip token mismatch: %q != %q", parsed.Token, bundle.Token)
	}
	if parsed.SpannerID != "test-spanner" {
		t.Errorf("round-trip spanner_id = %q", parsed.SpannerID)
	}

	if err := VerifyBundle(parsed); err != nil {
		t.Errorf("verify round-tripped bundle: %v", err)
	}
}

func TestVerifyBundleRejectsTampered(t *testing.T) {
	_, priv := genKey(t)

	bundle, err := MintInviteBundle(priv, "test-spanner", "https://srv1:8443", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	// Tamper with the token after signing.
	tampered := *bundle
	tampered.Token = bundle.Token + "x"
	if err := VerifyBundle(&tampered); err == nil {
		t.Error("verify accepted a bundle with a tampered token")
	}

	// Tamper with the spanner ID.
	tampered2 := *bundle
	tampered2.SpannerID = "other-spanner"
	if err := VerifyBundle(&tampered2); err == nil {
		t.Error("verify accepted a bundle with a tampered spanner_id")
	}

	// Tamper with the peer address.
	tampered3 := *bundle
	tampered3.PeerAddr = "https://evil:8443"
	if err := VerifyBundle(&tampered3); err == nil {
		t.Error("verify accepted a bundle with a tampered peer_addr")
	}
}

func TestVerifyBundleRejectsExpired(t *testing.T) {
	_, priv := genKey(t)

	// Negative TTL → already expired.
	bundle, err := MintInviteBundle(priv, "test-spanner", "https://srv1:8443", -time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := VerifyBundle(bundle); err == nil {
		t.Error("verify accepted an expired bundle")
	}
}

func TestVerifyBundleRejectsWrongFounder(t *testing.T) {
	_, priv := genKey(t)
	otherPub, _ := genKey(t)

	bundle, err := MintInviteBundle(priv, "test-spanner", "https://srv1:8443", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	// Swap the embedded founder pubkey — signature no longer matches.
	bundle.FounderPubkey = []byte(otherPub)
	if err := VerifyBundle(bundle); err == nil {
		t.Error("verify accepted a bundle whose founder pubkey was swapped")
	}
}

func TestTokenHashStable(t *testing.T) {
	h1 := TokenHash("some-token")
	h2 := TokenHash("some-token")
	if h1 != h2 {
		t.Errorf("TokenHash not deterministic: %q != %q", h1, h2)
	}
	if h1 == TokenHash("other-token") {
		t.Error("TokenHash collided for distinct tokens")
	}
	if len(h1) != 64 {
		t.Errorf("TokenHash length = %d, want 64 hex chars", len(h1))
	}
}

func TestAdmissionRowRoundTrip(t *testing.T) {
	pub, priv := genKey(t)

	row := &AdmissionRow{
		Pubkey:            "abcd1234",
		Hostname:          "srv1.example.com",
		AdmittedAt:        time.Now().UnixMilli(),
		AdmittedBy:        "abcd1234",
		ConsumedTokenHash: "",
		PatternClaims:     `["*.us-west.*"]`,
	}

	sig, err := SignAdmissionRow(priv, row)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if err := VerifyAdmissionRow(pub, row, sig); err != nil {
		t.Errorf("verify a freshly signed row: %v", err)
	}
}

func TestVerifyAdmissionRowRejectsTampered(t *testing.T) {
	pub, priv := genKey(t)

	row := &AdmissionRow{
		Pubkey:            "abcd1234",
		Hostname:          "srv1.example.com",
		AdmittedAt:        time.Now().UnixMilli(),
		AdmittedBy:        "ffff0000",
		ConsumedTokenHash: "deadbeef",
		PatternClaims:     `["*.us-west.*"]`,
	}
	sig, err := SignAdmissionRow(priv, row)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Tamper hostname.
	tampered := *row
	tampered.Hostname = "evil.example.com"
	if err := VerifyAdmissionRow(pub, &tampered, sig); err == nil {
		t.Error("verify accepted a row with a tampered hostname")
	}

	// Tamper pattern claims.
	tampered2 := *row
	tampered2.PatternClaims = `["*"]`
	if err := VerifyAdmissionRow(pub, &tampered2, sig); err == nil {
		t.Error("verify accepted a row with tampered pattern claims")
	}

	// Wrong admitter pubkey.
	otherPub, _ := genKey(t)
	if err := VerifyAdmissionRow(otherPub, row, sig); err == nil {
		t.Error("verify accepted a row against the wrong admitter pubkey")
	}
}
