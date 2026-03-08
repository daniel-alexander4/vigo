package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const keyFilename = "agent.key"
const tlsKeyFilename = "tls.key"
const tlsCertFilename = "tls.crt"
const caCertFilename = "ca.crt"

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

// GenerateTLSKey generates an ECDSA P-256 private key for TLS client certs.
func GenerateTLSKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// GenerateCSR creates a PEM-encoded X.509 certificate signing request
// for the given hostname using an ECDSA key.
func GenerateCSR(key *ecdsa.PrivateKey, hostname string) ([]byte, error) {
	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: hostname,
		},
		DNSNames: []string{hostname},
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		return nil, fmt.Errorf("create CSR: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	}), nil
}

// SaveTLSKey saves an ECDSA private key in PKCS8 PEM format to {dir}/tls.key.
func SaveTLSKey(dir string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal TLS key: %w", err)
	}

	block := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	}

	path := filepath.Join(dir, tlsKeyFilename)
	return os.WriteFile(path, pem.EncodeToMemory(block), 0600)
}

// SaveTLSCert saves a PEM-encoded certificate to {dir}/tls.crt.
func SaveTLSCert(dir string, certPEM []byte) error {
	return os.WriteFile(filepath.Join(dir, tlsCertFilename), certPEM, 0644)
}

// SaveCACert saves a PEM-encoded CA certificate to {dir}/ca.crt.
func SaveCACert(dir string, caPEM []byte) error {
	return os.WriteFile(filepath.Join(dir, caCertFilename), caPEM, 0644)
}

// TLSKeyPath returns the path to the TLS private key in the data directory.
func TLSKeyPath(dir string) string { return filepath.Join(dir, tlsKeyFilename) }

// TLSCertPath returns the path to the TLS certificate in the data directory.
func TLSCertPath(dir string) string { return filepath.Join(dir, tlsCertFilename) }

// CACertPath returns the path to the CA certificate in the data directory.
func CACertPath(dir string) string { return filepath.Join(dir, caCertFilename) }

// HasBootstrapCerts checks if TLS cert, key, and CA cert exist in the data directory.
func HasBootstrapCerts(dir string) bool {
	for _, f := range []string{tlsKeyFilename, tlsCertFilename, caCertFilename} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return false
		}
	}
	return true
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
