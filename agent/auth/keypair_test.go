package auth_test

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/hobnob/hobnob/agent/auth"
)

func TestGenerateKeypair(t *testing.T) {
	pub, priv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Errorf("public key length = %d, want %d", len(pub), ed25519.PublicKeySize)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Errorf("private key length = %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	// Verify the keys are a matching pair by signing and verifying
	msg := []byte("test message")
	sig := ed25519.Sign(priv, msg)
	if !ed25519.Verify(pub, msg, sig) {
		t.Error("generated keypair does not produce valid signatures")
	}
}

func TestSaveAndLoadKeypair(t *testing.T) {
	dir := t.TempDir()

	pub, priv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}

	if err := auth.SaveKeypair(dir, priv); err != nil {
		t.Fatalf("SaveKeypair() error: %v", err)
	}

	// Verify file permissions are 0600
	info, err := os.Stat(filepath.Join(dir, "agent.key"))
	if err != nil {
		t.Fatalf("stat agent.key: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("agent.key permissions = %o, want 0600", perm)
	}

	loadedPub, loadedPriv, err := auth.LoadKeypair(dir)
	if err != nil {
		t.Fatalf("LoadKeypair() error: %v", err)
	}

	if !pub.Equal(loadedPub) {
		t.Error("loaded public key does not match original")
	}
	if !priv.Equal(loadedPriv) {
		t.Error("loaded private key does not match original")
	}
}

func TestLoadKeypairFileNotExist(t *testing.T) {
	dir := t.TempDir()

	_, _, err := auth.LoadKeypair(dir)
	if err == nil {
		t.Fatal("LoadKeypair() expected error for missing file, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("LoadKeypair() error type = %T, want os.PathError with not exist; got: %v", err, err)
	}
}

func TestPublicKeyPEM(t *testing.T) {
	pub, _, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}

	pemStr, err := auth.PublicKeyPEM(pub)
	if err != nil {
		t.Fatalf("PublicKeyPEM() error: %v", err)
	}

	// Verify it's valid PEM with correct block type
	block, rest := pem.Decode([]byte(pemStr))
	if block == nil {
		t.Fatal("PublicKeyPEM() did not produce valid PEM")
	}
	if len(rest) != 0 {
		t.Error("PublicKeyPEM() has trailing data after PEM block")
	}
	if block.Type != "PUBLIC KEY" {
		t.Errorf("PEM block type = %q, want %q", block.Type, "PUBLIC KEY")
	}

	// Verify it's parseable by x509.ParsePKIXPublicKey
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParsePKIXPublicKey() error: %v", err)
	}
	parsedPub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		t.Fatalf("parsed key type = %T, want ed25519.PublicKey", parsed)
	}
	if !pub.Equal(parsedPub) {
		t.Error("parsed public key does not match original")
	}
}

func TestPrivateKeyPEM(t *testing.T) {
	_, priv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}

	// Save and read back the raw PEM content
	dir := t.TempDir()
	if err := auth.SaveKeypair(dir, priv); err != nil {
		t.Fatalf("SaveKeypair() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "agent.key"))
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	// Verify it's valid PEM with correct block type
	block, rest := pem.Decode(data)
	if block == nil {
		t.Fatal("private key file did not contain valid PEM")
	}
	if len(rest) != 0 {
		t.Error("private key file has trailing data after PEM block")
	}
	if block.Type != "PRIVATE KEY" {
		t.Errorf("PEM block type = %q, want %q", block.Type, "PRIVATE KEY")
	}

	// Verify it's parseable by x509.ParsePKCS8PrivateKey
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParsePKCS8PrivateKey() error: %v", err)
	}
	parsedPriv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		t.Fatalf("parsed key type = %T, want ed25519.PrivateKey", parsed)
	}
	if !priv.Equal(parsedPriv) {
		t.Error("parsed private key does not match original")
	}
}

func TestLoadOrGenerate_NewKeypair(t *testing.T) {
	dir := t.TempDir()

	pub, priv, err := auth.LoadOrGenerate(dir)
	if err != nil {
		t.Fatalf("LoadOrGenerate() error: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Errorf("public key length = %d, want %d", len(pub), ed25519.PublicKeySize)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Errorf("private key length = %d, want %d", len(priv), ed25519.PrivateKeySize)
	}

	// Key file should exist now
	if _, err := os.Stat(filepath.Join(dir, "agent.key")); err != nil {
		t.Errorf("agent.key should exist after LoadOrGenerate: %v", err)
	}
}

func TestLoadOrGenerate_ExistingKeypair(t *testing.T) {
	dir := t.TempDir()

	// Generate and save first
	origPub, origPriv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}
	if err := auth.SaveKeypair(dir, origPriv); err != nil {
		t.Fatalf("SaveKeypair() error: %v", err)
	}

	// LoadOrGenerate should load, not regenerate
	loadedPub, loadedPriv, err := auth.LoadOrGenerate(dir)
	if err != nil {
		t.Fatalf("LoadOrGenerate() error: %v", err)
	}
	if !origPub.Equal(loadedPub) {
		t.Error("LoadOrGenerate returned different public key than saved")
	}
	if !origPriv.Equal(loadedPriv) {
		t.Error("LoadOrGenerate returned different private key than saved")
	}
}
