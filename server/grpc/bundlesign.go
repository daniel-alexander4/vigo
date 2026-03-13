// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/protobuf/proto"
)

// BundleSigner signs PolicyBundle protos using an ED25519 private key.
// The signature covers fields 1-9 (policy content); fields 10-12 (signature,
// key ID, max_age) are set after signing.
type BundleSigner struct {
	privKey ed25519.PrivateKey
	pubKey  ed25519.PublicKey
	keyID   string // first 8 bytes of SHA-256(pubkey), hex-encoded
}

// NewBundleSigner creates a signer from an ED25519 private key.
func NewBundleSigner(privKey ed25519.PrivateKey) *BundleSigner {
	pubKey := privKey.Public().(ed25519.PublicKey)
	hash := sha256.Sum256(pubKey)
	keyID := hex.EncodeToString(hash[:8])
	return &BundleSigner{
		privKey: privKey,
		pubKey:  pubKey,
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
	sig := ed25519.Sign(bs.privKey, contentBytes)
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
	return ed25519.Verify(pubKey, contentBytes, bundle.Signature)
}

// publicKeyToPEM encodes an ED25519 public key to PKIX PEM format.
func publicKeyToPEM(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// marshalBundleContent serializes the policy content fields (1-9) for signing.
// Creates a copy with signing fields zeroed so we sign only content.
func marshalBundleContent(bundle *pb.PolicyBundle) ([]byte, error) {
	content := &pb.PolicyBundle{
		PolicyVersion:   bundle.PolicyVersion,
		Modules:         bundle.Modules,
		VarsJson:        bundle.VarsJson,
		VarOrder:        bundle.VarOrder,
		ConditionalVars: bundle.ConditionalVars,
		CustomTraits:    bundle.CustomTraits,
		MatchedPattern:  bundle.MatchedPattern,
		Environment:     bundle.Environment,
		Roles:           bundle.Roles,
		// Fields 10-12 intentionally omitted (zeroed).
	}
	return proto.Marshal(content)
}
