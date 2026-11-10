// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package spanner

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

func TestSignVerifyPeerIdentityRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	req := &PeerAdmitRequest{
		JoinerPubkey:  hex.EncodeToString(pub),
		Hostname:      "joiner.example.com",
		PatternClaims: []string{"*.us-east.*", "*.dc2.*"},
		SpannerID:     "test-spanner",
	}
	sig, err := SignPeerIdentity(priv, req)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req.Signature = sig

	got, err := VerifyPeerIdentity(req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !got.Equal(pub) {
		t.Error("verify returned a different pubkey than the signer")
	}
}

func TestVerifyPeerIdentityRejectsTamper(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	req := &PeerAdmitRequest{
		JoinerPubkey:  hex.EncodeToString(pub),
		Hostname:      "joiner.example.com",
		PatternClaims: []string{"*.us-east.*"},
		SpannerID:     "test-spanner",
	}
	sig, _ := SignPeerIdentity(priv, req)
	req.Signature = sig

	// Tamper each signed field in turn — every mutation must fail verify.
	for _, mut := range []func(r *PeerAdmitRequest){
		func(r *PeerAdmitRequest) { r.Hostname = "evil.example.com" },
		func(r *PeerAdmitRequest) { r.SpannerID = "other-spanner" },
		func(r *PeerAdmitRequest) { r.PatternClaims = []string{"*"} },
	} {
		tampered := *req
		mut(&tampered)
		if _, err := VerifyPeerIdentity(&tampered); err == nil {
			t.Error("verify accepted a tampered identity")
		}
	}

	// A signature made by a different key must fail (pubkey-substitution).
	otherPub, _, _ := ed25519.GenerateKey(nil)
	imposter := *req
	imposter.JoinerPubkey = hex.EncodeToString(otherPub)
	if _, err := VerifyPeerIdentity(&imposter); err == nil {
		t.Error("verify accepted a signature against a substituted pubkey")
	}
}

func TestNormalizePeerBase(t *testing.T) {
	cases := []struct {
		peer, defaultPort, want string
	}{
		{"10.0.0.2", "8443", "https://10.0.0.2:8443"},
		{"bolt-3.dc1:9443", "8443", "https://bolt-3.dc1:9443"},
		{"https://host:8443", "8443", "https://host:8443"},
		{"https://host:8443/", "8443", "https://host:8443"},
		{"http://127.0.0.1:1234", "8443", "http://127.0.0.1:1234"},
		{"  10.0.0.5  ", "8443", "https://10.0.0.5:8443"},
	}
	for _, c := range cases {
		if got := normalizePeerBase(c.peer, c.defaultPort); got != c.want {
			t.Errorf("normalizePeerBase(%q, %q) = %q, want %q", c.peer, c.defaultPort, got, c.want)
		}
	}
}

func TestRosterHasFounderSelfAdmit(t *testing.T) {
	founder := "aa11"
	rows := []RosterRow{
		{Pubkey: founder, AdmittedBy: founder, ConsumedTokenHash: ""},       // founder self-admit
		{Pubkey: "bb22", AdmittedBy: founder, ConsumedTokenHash: "tok"},     // joiner via token
		{Pubkey: "cc33", AdmittedBy: founder, ConsumedTokenHash: ""},        // peer-admitted joiner
	}
	if !RosterHasFounderSelfAdmit(rows, founder) {
		t.Error("expected founder self-admit row to be found")
	}
	// A peer-admitted joiner (admitted_by == founder, empty token, but
	// pubkey != founder) must NOT count as a founder self-admit.
	if RosterHasFounderSelfAdmit(rows, "cc33") {
		t.Error("a peer-admitted row was misidentified as a founder self-admit")
	}
	if RosterHasFounderSelfAdmit(rows, "dd44") {
		t.Error("a pubkey absent from the roster was reported as founder")
	}
}
