package auth

import (
	"crypto/ed25519"
)

// GenerateKeypair generates a new ED25519 keypair.
func GenerateKeypair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return nil, nil, nil
}

// SaveKeypair saves an ED25519 private key in PKCS8 PEM format to the given directory.
func SaveKeypair(dir string, priv ed25519.PrivateKey) error {
	return nil
}

// LoadKeypair loads an ED25519 keypair from a PKCS8 PEM file in the given directory.
func LoadKeypair(dir string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return nil, nil, nil
}

// PublicKeyPEM exports an ED25519 public key in PKIX PEM format.
func PublicKeyPEM(pub ed25519.PublicKey) (string, error) {
	return "", nil
}

// LoadOrGenerate attempts to load an existing keypair; if not found, generates and saves a new one.
func LoadOrGenerate(dir string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return nil, nil, nil
}
