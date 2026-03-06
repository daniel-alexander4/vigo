package auth

import (
	"crypto/ed25519"
)

// SignPayload signs payload+timestamp with the given ED25519 private key.
// The timestamp is concatenated with the payload before signing to prevent
// replay attacks (the server enforces a 5-minute window).
func SignPayload(payload []byte, timestamp string, key ed25519.PrivateKey) []byte {
	msg := buildSignedMessage(payload, timestamp)
	return ed25519.Sign(key, msg)
}

// VerifyPayload verifies an ED25519 signature over payload+timestamp
// using the given public key. Returns true if the signature is valid.
func VerifyPayload(payload []byte, timestamp string, sig []byte, pub ed25519.PublicKey) bool {
	msg := buildSignedMessage(payload, timestamp)
	return ed25519.Verify(pub, msg, sig)
}

// buildSignedMessage concatenates payload and timestamp bytes to form
// the message that is signed/verified.
func buildSignedMessage(payload []byte, timestamp string) []byte {
	msg := make([]byte, len(payload)+len(timestamp))
	copy(msg, payload)
	copy(msg[len(payload):], timestamp)
	return msg
}
