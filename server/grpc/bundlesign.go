// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	voied25519 "github.com/oasisprotocol/curve25519-voi/primitives/ed25519"
	"google.golang.org/protobuf/proto"
)

// BundleSigner signs PolicyBundle protos using an ED25519 private key.
// The signature covers fields 1-9 + 15 (policy content + merkle root);
// fields 10-12 (signature, key ID, max_age) are set after signing.
//
// Both keys are held as voi types so Sign and VerifyBundle use voi's
// AVX2-accelerated implementations on amd64. The aliasing is free —
// voi.PrivateKey / voi.PublicKey are []byte under the hood, same as
// stdlib's, so the conversion is a no-op slice reinterpretation.
type BundleSigner struct {
	privKey ed25519.PrivateKey   // kept for PEM export / Public() interface
	pubKey  ed25519.PublicKey
	voiPriv voied25519.PrivateKey // signing-path alias (no allocation)
	keyID   string                // first 8 bytes of SHA-256(pubkey), hex-encoded
}

// NewBundleSigner creates a signer from an ED25519 private key.
func NewBundleSigner(privKey ed25519.PrivateKey) *BundleSigner {
	pubKey := privKey.Public().(ed25519.PublicKey)
	hash := sha256.Sum256(pubKey)
	keyID := hex.EncodeToString(hash[:8])
	return &BundleSigner{
		privKey: privKey,
		pubKey:  pubKey,
		voiPriv: voied25519.PrivateKey(privKey),
		keyID:   keyID,
	}
}

// Sign computes an ED25519 signature over the bundle's content fields (1-9)
// and sets the signature, key ID, and max_age_seconds on the bundle.
func (bs *BundleSigner) Sign(bundle *pb.PolicyBundle, maxAgeSeconds uint32) error {
	contentBytes, err := marshalBundleContent(bundle)
	if err != nil {
		return fmt.Errorf("marshaling bundle content: %w", err)
	}
	sig := voied25519.Sign(bs.voiPriv, contentBytes)
	bundle.Signature = sig
	bundle.ServerPubkeyId = bs.keyID
	bundle.MaxAgeSeconds = maxAgeSeconds
	return nil
}

// PublicKeyPEM returns the signer's public key in PEM format (for RegisterResponse).
func (bs *BundleSigner) PublicKeyPEM() (string, error) {
	return publicKeyToPEM(bs.pubKey)
}

// KeyID returns the hex-encoded key fingerprint.
func (bs *BundleSigner) KeyID() string {
	return bs.keyID
}

// VerifyBundle checks that a bundle's signature is valid against the given public key.
// Used in tests; the agent does verification in Rust.
func VerifyBundle(bundle *pb.PolicyBundle, pubKey ed25519.PublicKey) bool {
	contentBytes, err := marshalBundleContent(bundle)
	if err != nil {
		return false
	}
	return voied25519.Verify(voied25519.PublicKey(pubKey), contentBytes, bundle.Signature)
}

// publicKeyToPEM encodes an ED25519 public key to PKIX PEM format.
func publicKeyToPEM(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// marshalBundleContent serializes the policy content fields (1-9, 15) for signing.
// Creates a copy with signing fields zeroed so we sign only content.
func marshalBundleContent(bundle *pb.PolicyBundle) ([]byte, error) {
	content := &pb.PolicyBundle{
		PolicyVersion:   bundle.PolicyVersion,
		Modules:         bundle.Modules,
		VarsJson:        bundle.VarsJson,
		VarOrder:        bundle.VarOrder,
		CustomTraits:    bundle.CustomTraits,
		MatchedPattern:  bundle.MatchedPattern,
		Environment:     bundle.Environment,
		Roles:           bundle.Roles,
		MerkleRoot:      bundle.MerkleRoot,
		// Fields 10-14 intentionally omitted (zeroed).
	}
	return proto.Marshal(content)
}
