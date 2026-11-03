// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package spanner

import (
	"testing"
	"time"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/bolt"
)

// TestSignUserRow_RoundTrip pins the basic sign/verify contract for
// ADR-030 webuser rows — the foundation Slice 2-iii-d's merge layer
// builds on.
func TestSignUserRow_RoundTrip(t *testing.T) {
	pub, priv := genKey(t)

	row := &vigoauth.UserRow{
		ID:           "user-abc",
		Username:     "alice",
		DisplayName:  "Alice Admin",
		Email:        "alice@example.com",
		Role:         "admin",
		Permissions:  `["*"]`,
		SSHPublicKey: "ssh-ed25519 AAAA...",
		OIDCSubject:  "",
		UpdatedAtMs:  time.Now().UnixMilli(),
		OriginBolt:   "0123456789abcdef",
		DeletedAt:    0,
	}

	sig, err := SignUserRow(priv, row)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := VerifyUserRow(pub, row, sig); err != nil {
		t.Errorf("verify a freshly signed row: %v", err)
	}
}

// TestVerifyUserRow_RejectsTampered pins that field-level tampering
// breaks signature verification — the security property the merge
// layer depends on.
func TestVerifyUserRow_RejectsTampered(t *testing.T) {
	pub, priv := genKey(t)
	now := time.Now().UnixMilli()

	row := &vigoauth.UserRow{
		ID:          "user-xyz",
		Username:    "bob",
		Role:        "viewer",
		UpdatedAtMs: now,
		OriginBolt:  "feeddead",
	}
	sig, err := SignUserRow(priv, row)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Role escalation: viewer → admin must not verify.
	escalated := *row
	escalated.Role = "admin"
	if err := VerifyUserRow(pub, &escalated, sig); err == nil {
		t.Error("verify accepted a row with a tampered role (privilege escalation)")
	}

	// Tombstone-flip: live → deleted must not verify.
	tombstoned := *row
	tombstoned.DeletedAt = now
	if err := VerifyUserRow(pub, &tombstoned, sig); err == nil {
		t.Error("verify accepted a row with a forged tombstone")
	}

	// Wrong key: anyone else's key must not verify alice's row.
	otherPub, _ := genKey(t)
	if err := VerifyUserRow(otherPub, row, sig); err == nil {
		t.Error("verify accepted a row signed by a different key")
	}
}

// TestBoltSigner_StampsOrigin pins the auth.Signer adapter: signing
// through BoltSigner sets the row's OriginBolt to the signing bolt's
// pubkey-hex before sealing it, so peers verify the same canonical
// content the local store records.
func TestBoltSigner_StampsOrigin(t *testing.T) {
	pub, priv := genKey(t)
	id := &bolt.Identity{PublicKey: pub, SigningKey: priv}
	signer := NewBoltSigner(id)
	if signer == nil {
		t.Fatal("NewBoltSigner returned nil for non-nil identity")
	}

	row := &vigoauth.UserRow{
		ID:          "user-1",
		Username:    "alice",
		Role:        "admin",
		UpdatedAtMs: time.Now().UnixMilli(),
	}
	originHex, sig, err := signer.Sign(row)
	if err != nil {
		t.Fatalf("BoltSigner.Sign: %v", err)
	}

	expectedHex := hexEncode(pub)
	if originHex != expectedHex {
		t.Errorf("origin hex mismatch: got %q want %q", originHex, expectedHex)
	}
	if row.OriginBolt != expectedHex {
		t.Errorf("BoltSigner.Sign must stamp row.OriginBolt; got %q want %q", row.OriginBolt, expectedHex)
	}

	// The returned signature must verify against the same pubkey over
	// the now-stamped row.
	if err := VerifyUserRow(pub, row, sig); err != nil {
		t.Errorf("verify the stamped+signed row: %v", err)
	}
}

// TestNewBoltSigner_NilIdentity is the standalone-mode contract: a
// nil identity yields a nil signer so callers can pass the result
// straight into UserStore.SetSigner without an extra nil-check.
func TestNewBoltSigner_NilIdentity(t *testing.T) {
	if got := NewBoltSigner(nil); got != nil {
		t.Errorf("NewBoltSigner(nil) = %v, want nil", got)
	}
}

func hexEncode(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0xf]
	}
	return string(out)
}
