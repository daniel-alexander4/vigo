// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package spanner

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Spanner Slice 2A.2 — the membership ceremony (ADR-026 + the
// spanner-redesign plan). This file owns the two signed objects the
// ceremony moves over hand-written REST:
//
//   1. AdmissionRow — the canonical content of a spanner_bolts row,
//      signed by the admitting bolt's Ed25519 identity. Every roster
//      row carries this signature so a joining peer can verify the
//      whole roster end-to-end rather than trusting the wire.
//
//   2. InviteBundle — the join token a founding/inviting bolt mints
//      and hands (out of band) to a prospective joiner. The bundle
//      is signed by the inviter's bolt identity over its content
//      fields; the joiner forwards it back to the inviter's /admit
//      endpoint, which verifies the signature + freshness and
//      consumes the token exactly once.
//
// Canonical-marshal discipline mirrors marshalReportContent /
// VerifyReportSignature in reporter.go: build a content-only value
// with the signature field cleared, marshal it deterministically,
// then ed25519.Sign / ed25519.Verify over those bytes. JSON with
// sorted keys (encoding/json sorts struct fields by declaration and
// map keys lexically) is deterministic for the fixed struct shapes
// used here.

// InviteBundle is the signed join token a founding/inviting bolt
// mints for a prospective joiner. Token is a random 256-bit secret;
// its sha256 is the replay-protection identity recorded in
// spanner_consumed_tokens. FounderPubkey is the raw 32-byte Ed25519
// pubkey of the inviting bolt — the bundle's Signature verifies
// against it, and the joiner uses it to know which peer to call.
type InviteBundle struct {
	Token         string `json:"token"`          // random 256-bit secret, base64 std
	SpannerID     string `json:"spanner_id"`     // federation anchor
	PeerAddr      string `json:"peer_addr"`      // inviting vigosrv's REST base URL
	ExpiresAt     int64  `json:"expires_at"`     // ms-since-epoch; past → rejected
	FounderPubkey []byte `json:"founder_pubkey"` // raw 32-byte Ed25519 pubkey of the inviter
	Signature     []byte `json:"signature"`      // ed25519 over the content fields
}

// inviteBundleContent is the canonical content of an InviteBundle —
// every field except Signature. Marshalled deterministically for
// sign/verify.
type inviteBundleContent struct {
	Token         string `json:"token"`
	SpannerID     string `json:"spanner_id"`
	PeerAddr      string `json:"peer_addr"`
	ExpiresAt     int64  `json:"expires_at"`
	FounderPubkey []byte `json:"founder_pubkey"`
}

// marshalBundleContent serializes an InviteBundle's content fields
// (Signature cleared) for signing/verification.
func marshalBundleContent(b *InviteBundle) ([]byte, error) {
	return json.Marshal(inviteBundleContent{
		Token:         b.Token,
		SpannerID:     b.SpannerID,
		PeerAddr:      b.PeerAddr,
		ExpiresAt:     b.ExpiresAt,
		FounderPubkey: b.FounderPubkey,
	})
}

// MintInviteBundle generates a fresh random 256-bit token, fills the
// bundle, and signs it with the inviting bolt's signing key. ttl is
// how long the bundle stays valid from now.
func MintInviteBundle(signingKey ed25519.PrivateKey, spannerID, peerAddr string, ttl time.Duration) (*InviteBundle, error) {
	if len(signingKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invite mint: signing key is %d bytes, expected %d", len(signingKey), ed25519.PrivateKeySize)
	}
	pub, ok := signingKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("invite mint: signing key has no Ed25519 public half")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("invite mint: generating token: %w", err)
	}

	b := &InviteBundle{
		Token:         base64.StdEncoding.EncodeToString(raw),
		SpannerID:     spannerID,
		PeerAddr:      peerAddr,
		ExpiresAt:     time.Now().Add(ttl).UnixMilli(),
		FounderPubkey: []byte(pub),
	}
	content, err := marshalBundleContent(b)
	if err != nil {
		return nil, fmt.Errorf("invite mint: canonical marshal: %w", err)
	}
	b.Signature = ed25519.Sign(signingKey, content)
	return b, nil
}

// MarshalBundle encodes an InviteBundle as a single opaque string
// (base64 of its JSON) for CLI transport — the operator copy-pastes
// this between vigosrvs.
func MarshalBundle(b *InviteBundle) (string, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("marshal bundle: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// ParseBundle decodes the string produced by MarshalBundle. It does
// NOT verify the signature — call VerifyBundle for that.
func ParseBundle(s string) (*InviteBundle, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("parse bundle: base64 decode: %w", err)
	}
	var b InviteBundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("parse bundle: json decode: %w", err)
	}
	return &b, nil
}

// VerifyBundle checks an InviteBundle's signature against the
// FounderPubkey embedded in the bundle, and rejects an expired
// bundle. Returns nil iff the bundle is well-formed, signed by the
// holder of FounderPubkey, and not past ExpiresAt.
func VerifyBundle(b *InviteBundle) error {
	if len(b.FounderPubkey) != ed25519.PublicKeySize {
		return fmt.Errorf("invite verify: founder_pubkey is %d bytes, expected %d", len(b.FounderPubkey), ed25519.PublicKeySize)
	}
	if len(b.Signature) != ed25519.SignatureSize {
		return fmt.Errorf("invite verify: signature is %d bytes, expected %d", len(b.Signature), ed25519.SignatureSize)
	}
	if b.Token == "" {
		return fmt.Errorf("invite verify: empty token")
	}
	content, err := marshalBundleContent(b)
	if err != nil {
		return fmt.Errorf("invite verify: canonical marshal: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(b.FounderPubkey), content, b.Signature) {
		return fmt.Errorf("invite verify: ed25519 verify failed")
	}
	if time.Now().UnixMilli() > b.ExpiresAt {
		return fmt.Errorf("invite verify: bundle expired at %d ms", b.ExpiresAt)
	}
	return nil
}

// TokenHash returns the hex-encoded sha256 of an invite token — the
// replay-protection identity recorded in spanner_consumed_tokens.
// Both the consumer-side INSERT and the IsTokenConsumed check key on
// this value, never the raw token.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// AdmissionRow is the canonical content of a spanner_bolts row that
// the admitting bolt signs. The signature covers identity + grant
// fields only — revoked_* and last_snapshot_at are deliberately
// excluded so a later revoke (a separate signed action under the
// CRDT in Slice 2) does not invalidate the original admission
// signature. ConsumedTokenHash is empty for a founder self-admit.
type AdmissionRow struct {
	Pubkey            string `json:"pubkey"`
	Hostname          string `json:"hostname"`
	AdmittedAt        int64  `json:"admitted_at"`
	AdmittedBy        string `json:"admitted_by"`
	ConsumedTokenHash string `json:"consumed_token_hash"`
	PatternClaims     string `json:"pattern_claims"`
}

// marshalAdmissionContent serializes an AdmissionRow deterministically
// for signing/verification.
func marshalAdmissionContent(row *AdmissionRow) ([]byte, error) {
	return json.Marshal(row)
}

// SignAdmissionRow signs an AdmissionRow's canonical content with the
// admitting bolt's signing key. The returned bytes are stored in
// spanner_bolts.signature.
func SignAdmissionRow(signingKey ed25519.PrivateKey, row *AdmissionRow) ([]byte, error) {
	if len(signingKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("admission sign: signing key is %d bytes, expected %d", len(signingKey), ed25519.PrivateKeySize)
	}
	content, err := marshalAdmissionContent(row)
	if err != nil {
		return nil, fmt.Errorf("admission sign: canonical marshal: %w", err)
	}
	return ed25519.Sign(signingKey, content), nil
}

// VerifyAdmissionRow checks that sig is a valid signature over row's
// canonical content, made by the holder of admitterPubkey (the raw
// 32-byte Ed25519 pubkey of the bolt named in row.AdmittedBy).
// Returns nil on a valid signature.
func VerifyAdmissionRow(admitterPubkey ed25519.PublicKey, row *AdmissionRow, sig []byte) error {
	if len(admitterPubkey) != ed25519.PublicKeySize {
		return fmt.Errorf("admission verify: admitter pubkey is %d bytes, expected %d", len(admitterPubkey), ed25519.PublicKeySize)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("admission verify: signature is %d bytes, expected %d", len(sig), ed25519.SignatureSize)
	}
	content, err := marshalAdmissionContent(row)
	if err != nil {
		return fmt.Errorf("admission verify: canonical marshal: %w", err)
	}
	if !ed25519.Verify(admitterPubkey, content, sig) {
		return fmt.Errorf("admission verify: ed25519 verify failed")
	}
	return nil
}
