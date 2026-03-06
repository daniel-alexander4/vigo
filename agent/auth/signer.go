package auth

import (
	"crypto/ed25519"
)

// SignPayload signs payload+timestamp with the given private key, producing an ED25519 signature.
// The timestamp is included in the signed data to prevent replay attacks.
func SignPayload(payload []byte, timestamp string, key ed25519.PrivateKey) []byte {
	return nil
}

// VerifyPayload verifies an ED25519 signature over payload+timestamp using the given public key.
func VerifyPayload(payload []byte, timestamp string, sig []byte, pub ed25519.PublicKey) bool {
	return false
}
