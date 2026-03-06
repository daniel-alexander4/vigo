package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const keyFilename = "agent.key"

// GenerateKeypair generates a new ED25519 keypair.
func GenerateKeypair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ed25519 keypair: %w", err)
	}
	return pub, priv, nil
}

// SaveKeypair saves an ED25519 private key in PKCS8 PEM format to {dir}/agent.key
// with 0600 permissions.
func SaveKeypair(dir string, priv ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal private key to PKCS8: %w", err)
	}

	block := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	}

	path := filepath.Join(dir, keyFilename)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		return fmt.Errorf("write private key file: %w", err)
	}

	return nil
}

// LoadKeypair loads an ED25519 keypair from a PKCS8 PEM file in the given directory.
// Returns an os.PathError wrapping os.ErrNotExist if the key file does not exist.
func LoadKeypair(dir string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	path := filepath.Join(dir, keyFilename)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, nil, errors.New("no PEM block found in key file")
	}
	if block.Type != "PRIVATE KEY" {
		return nil, nil, fmt.Errorf("unexpected PEM block type %q, want %q", block.Type, "PRIVATE KEY")
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse PKCS8 private key: %w", err)
	}

	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("key is %T, not ed25519.PrivateKey", parsed)
	}

	pub := priv.Public().(ed25519.PublicKey)
	return pub, priv, nil
}

// PublicKeyPEM exports an ED25519 public key in PKIX PEM format.
func PublicKeyPEM(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal public key to PKIX: %w", err)
	}

	block := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: der,
	}

	return string(pem.EncodeToMemory(block)), nil
}

// LoadOrGenerate attempts to load an existing keypair from the directory.
// If the key file does not exist, it generates a new keypair and saves it.
func LoadOrGenerate(dir string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := LoadKeypair(dir)
	if err == nil {
		return pub, priv, nil
	}
	if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("load keypair: %w", err)
	}

	// Key file doesn't exist -- generate new keypair
	pub, priv, err = GenerateKeypair()
	if err != nil {
		return nil, nil, err
	}

	if err := SaveKeypair(dir, priv); err != nil {
		return nil, nil, err
	}

	return pub, priv, nil
}
