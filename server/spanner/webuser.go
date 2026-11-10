// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package spanner

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/bolt"
)

// ADR-030 Slice 2-iii-c — sign / verify primitives + BoltSigner.
//
// Mirrors server/spanner/admission.go's pattern: a canonical content
// struct (auth.UserRow) is marshalled deterministically with
// encoding/json, then ed25519.Sign'd by the origin bolt's identity.
// Peers reconstruct UserRow from the wire shape and verify against the
// origin bolt's pubkey looked up in the local roster — same end-to-end
// verification shape MergeSnapshot uses for spanner_bolts rows.
//
// No domain separator on the signature input. Matches admission.go.
// If we ever need cross-message-type collision resistance for
// bolt-identity signing, do it across admissions + webusers + any
// future-types in one refactor.

// marshalUserRowContent serializes a UserRow deterministically for
// signing / verification. encoding/json preserves struct field order,
// and UserRow has no maps, so the byte stream is stable across builds.
func marshalUserRowContent(row *vigoauth.UserRow) ([]byte, error) {
	return json.Marshal(row)
}

// SignUserRow signs a UserRow's canonical content with the origin
// bolt's signing key. The returned bytes are stored verbatim in
// users.signature and served on the gossip snapshot.
func SignUserRow(signingKey ed25519.PrivateKey, row *vigoauth.UserRow) ([]byte, error) {
	if len(signingKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("webuser sign: signing key is %d bytes, expected %d", len(signingKey), ed25519.PrivateKeySize)
	}
	content, err := marshalUserRowContent(row)
	if err != nil {
		return nil, fmt.Errorf("webuser sign: canonical marshal: %w", err)
	}
	return ed25519.Sign(signingKey, content), nil
}

// VerifyUserRow checks sig against row's canonical content, made by
// the holder of originPubkey (raw 32-byte Ed25519 of the bolt named
// in row.OriginBolt). Returns nil on a valid signature; the caller
// drops the row if verify fails.
func VerifyUserRow(originPubkey ed25519.PublicKey, row *vigoauth.UserRow, sig []byte) error {
	if len(originPubkey) != ed25519.PublicKeySize {
		return fmt.Errorf("webuser verify: origin pubkey is %d bytes, expected %d", len(originPubkey), ed25519.PublicKeySize)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("webuser verify: signature is %d bytes, expected %d", len(sig), ed25519.SignatureSize)
	}
	content, err := marshalUserRowContent(row)
	if err != nil {
		return fmt.Errorf("webuser verify: canonical marshal: %w", err)
	}
	if !ed25519.Verify(originPubkey, content, sig) {
		return fmt.Errorf("webuser verify: ed25519 verify failed")
	}
	return nil
}

// BoltSigner adapts a *bolt.Identity into the auth.Signer interface
// UserStore consumes on every write. Constructed in server.go when
// the bolt identity is in scope; passed to NewUserStore in spanner
// mode. Standalone mode passes nil — UserStore writes proceed without
// origin_bolt/signature stamping.
type BoltSigner struct {
	Identity   *bolt.Identity
	pubkeyHex  string // memoized hex projection of Identity.PublicKey
}

// NewBoltSigner constructs a BoltSigner. Returns nil if id is nil so
// callers can pass the result directly to NewUserStore (which accepts
// nil Signer for standalone mode) — no separate nil-check needed.
func NewBoltSigner(id *bolt.Identity) *BoltSigner {
	if id == nil {
		return nil
	}
	return &BoltSigner{
		Identity:  id,
		pubkeyHex: hex.EncodeToString(id.PublicKey),
	}
}

// Sign implements auth.Signer. Stamps the origin bolt before signing
// so the canonical content's OriginBolt field is what gets covered by
// the signature — peers verify the same bytes.
func (s *BoltSigner) Sign(row *vigoauth.UserRow) (string, []byte, error) {
	row.OriginBolt = s.pubkeyHex
	sig, err := SignUserRow(s.Identity.SigningKey, row)
	if err != nil {
		return "", nil, err
	}
	return s.pubkeyHex, sig, nil
}
