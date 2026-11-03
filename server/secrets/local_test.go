// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupLocalProvider(t *testing.T) (*LocalProvider, string) {
	t.Helper()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")
	secretsDir := filepath.Join(dir, "secrets")

	// Generate a deterministic test key (32 bytes hex-encoded).
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	os.WriteFile(keyFile, []byte(hex.EncodeToString(key)+"\n"), 0o600)

	p, err := NewLocalProvider(keyFile, secretsDir)
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	return p, dir
}

func TestLocalProviderSetAndGet(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	if err := p.Set("vigo/db/dsn", "my-database.db"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "vigo/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "my-database.db" {
		t.Errorf("Get = %q, want %q", val, "my-database.db")
	}
}

func TestLocalProviderSetTrimsWhitespace(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Simulate copy-paste with trailing newline, tabs, spaces.
	if err := p.Set("vigo/api/key", "  sk-abc123\n\t "); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "vigo/api/key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "sk-abc123" {
		t.Errorf("Get = %q, want %q (whitespace should be trimmed)", val, "sk-abc123")
	}
}

func TestLocalProviderSetTrimsCarriageReturn(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Windows-style line ending from copy-paste.
	if err := p.Set("vigo/api/key", "mypassword\r\n"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "vigo/api/key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "mypassword" {
		t.Errorf("Get = %q, want %q", val, "mypassword")
	}
}

func TestLocalProviderSetStripsBOM(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// UTF-8 BOM from Windows Notepad copy-paste.
	if err := p.Set("vigo/api/key", "\xEF\xBB\xBFsk-abc123"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "vigo/api/key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "sk-abc123" {
		t.Errorf("Get = %q, want %q (BOM should be stripped)", val, "sk-abc123")
	}
}

func TestLocalProviderSetStripsZeroWidth(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Zero-width space from Slack/web paste.
	if err := p.Set("vigo/api/key", "sk-abc\u200B123"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "vigo/api/key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "sk-abc123" {
		t.Errorf("Get = %q, want %q (zero-width space should be stripped)", val, "sk-abc123")
	}
}

func TestLocalProviderSetStripsNullAndBidi(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Null byte + bidi override (Trojan Source style).
	if err := p.Set("vigo/api/key", "pass\x00word\u202Etest"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "vigo/api/key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "passwordtest" {
		t.Errorf("Get = %q, want %q (null and bidi should be stripped)", val, "passwordtest")
	}
}

func TestLocalProviderSetNBSPToSpace(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Non-breaking space from web page copy-paste — converted to regular space, then trimmed.
	if err := p.Set("vigo/api/key", "\u00A0sk-abc123\u00A0"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "vigo/api/key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "sk-abc123" {
		t.Errorf("Get = %q, want %q (NBSP should become space then trim)", val, "sk-abc123")
	}
}

func TestLocalProviderGetBytes(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	if err := p.Set("vigo/tls/cert", "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.GetBytes(context.Background(), "vigo/tls/cert")
	if err != nil {
		t.Fatalf("GetBytes: %v", err)
	}
	if len(val) == 0 {
		t.Error("GetBytes returned empty")
	}
}

func TestLocalProviderMissingKey(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	_, err := p.Get(context.Background(), "nonexistent/key")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestLocalProviderBuiltinFallback(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Core keys fall back to built-in defaults when not stored.
	val, err := p.Get(context.Background(), "vigo/db/dsn")
	if err != nil {
		t.Fatalf("Get builtin: %v", err)
	}
	if val != "/srv/vigo/db/vigo.db" {
		t.Errorf("builtin fallback = %q, want %q", val, "/srv/vigo/db/vigo.db")
	}
}

func TestLocalProviderOverridesBuiltin(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Stored value overrides built-in default.
	if err := p.Set("vigo/db/dsn", "custom.db"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "vigo/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "custom.db" {
		t.Errorf("Get = %q, want %q", val, "custom.db")
	}
}

func TestLocalProviderDelete(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	if err := p.Set("temp/secret", "value"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := p.Delete("temp/secret"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := p.Get(context.Background(), "temp/secret")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestLocalProviderDeleteNonexistent(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Deleting a nonexistent key should not error.
	if err := p.Delete("does/not/exist"); err != nil {
		t.Fatalf("Delete nonexistent: %v", err)
	}
}

func TestLocalProviderList(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	p.Set("alpha/one", "v1")
	p.Set("beta/two", "v2")
	p.Set("gamma", "v3")

	keys, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("List returned %d keys, want 3: %v", len(keys), keys)
	}

	// Check all keys are present.
	found := make(map[string]bool)
	for _, k := range keys {
		found[k] = true
	}
	for _, want := range []string{"alpha/one", "beta/two", "gamma"} {
		if !found[want] {
			t.Errorf("List missing %q, got %v", want, keys)
		}
	}
}

func TestLocalProviderListEmpty(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	keys, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("List returned %d keys, want 0", len(keys))
	}
}

func TestLocalProviderGetVersion(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Nonexistent key returns version 0.
	ver, err := p.GetVersion(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if ver != 0 {
		t.Errorf("version = %d, want 0 for missing key", ver)
	}

	// After setting, version should be > 0 (mtime unix seconds).
	if err := p.Set("versioned/key", "val"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	ver, err = p.GetVersion(context.Background(), "versioned/key")
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if ver <= 0 {
		t.Errorf("version = %d, want > 0 after set", ver)
	}
}

func TestLocalProviderPathTraversal(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	_, err := p.Get(context.Background(), "../../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}

	err = p.Set("../../../etc/evil", "data")
	if err == nil {
		t.Fatal("expected error for path traversal on set")
	}
}

func TestLocalProviderWrongKey(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Store with original key.
	if err := p.Set("test/secret", "sensitive"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Create a new provider with a different key.
	dir := filepath.Dir(p.secretsDir)
	keyFile2 := filepath.Join(dir, "wrong.key")
	wrongKey := make([]byte, 32)
	rand.Read(wrongKey)
	os.WriteFile(keyFile2, []byte(hex.EncodeToString(wrongKey)+"\n"), 0o600)

	p2, err := NewLocalProvider(keyFile2, p.secretsDir)
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	defer p2.Close()

	_, err = p2.Get(context.Background(), "test/secret")
	if err == nil {
		t.Fatal("expected decryption error with wrong key")
	}
}

func TestLocalProviderMissingKeyFile(t *testing.T) {
	_, err := NewLocalProvider("/nonexistent/key.file", t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing key file")
	}
}

// --- Auto-generated key tests (no key_file specified) ---

func setupAutoKeyProvider(t *testing.T) *LocalProvider {
	t.Helper()
	secretsDir := filepath.Join(t.TempDir(), "secrets")
	p, err := NewLocalProvider("", secretsDir)
	if err != nil {
		t.Fatalf("NewLocalProvider auto-key: %v", err)
	}
	return p
}

func TestAutoKeyProviderSetAndGet(t *testing.T) {
	p := setupAutoKeyProvider(t)
	defer p.Close()

	if err := p.Set("vigo/db/dsn", "my-database.db"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "vigo/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "my-database.db" {
		t.Errorf("Get = %q, want %q", val, "my-database.db")
	}
}

func TestAutoKeyProviderGeneratesKeyFile(t *testing.T) {
	secretsDir := filepath.Join(t.TempDir(), "secrets")
	p, err := NewLocalProvider("", secretsDir)
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	defer p.Close()

	keyFile := filepath.Join(secretsDir, ".master.key")
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("master key not created: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("master key perms = %o, want 0600", info.Mode().Perm())
	}

	data, _ := os.ReadFile(keyFile)
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) != 64 {
		t.Errorf("master key hex length = %d, want 64", len(trimmed))
	}
}

func TestAutoKeyProviderFileIsEncrypted(t *testing.T) {
	p := setupAutoKeyProvider(t)
	defer p.Close()

	if err := p.Set("test/secret", "hello-world"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The file on disk must NOT be readable as plaintext.
	path := filepath.Join(p.secretsDir, "test", "secret")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) == "hello-world" {
		t.Error("secret file is plaintext — should be encrypted")
	}
}

func TestAutoKeyProviderReusesExistingKey(t *testing.T) {
	secretsDir := filepath.Join(t.TempDir(), "secrets")

	// First provider auto-generates a key.
	p1, err := NewLocalProvider("", secretsDir)
	if err != nil {
		t.Fatalf("NewLocalProvider 1: %v", err)
	}
	if err := p1.Set("test/key", "secret-value"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	p1.Close()

	// Second provider should reuse the same key and decrypt successfully.
	p2, err := NewLocalProvider("", secretsDir)
	if err != nil {
		t.Fatalf("NewLocalProvider 2: %v", err)
	}
	defer p2.Close()

	val, err := p2.Get(context.Background(), "test/key")
	if err != nil {
		t.Fatalf("Get with reused key: %v", err)
	}
	if val != "secret-value" {
		t.Errorf("Get = %q, want %q", val, "secret-value")
	}
}

func TestAutoKeyProviderBuiltinFallback(t *testing.T) {
	p := setupAutoKeyProvider(t)
	defer p.Close()

	val, err := p.Get(context.Background(), "vigo/db/dsn")
	if err != nil {
		t.Fatalf("Get builtin: %v", err)
	}
	if val != "/srv/vigo/db/vigo.db" {
		t.Errorf("builtin fallback = %q, want %q", val, "/srv/vigo/db/vigo.db")
	}
}

func TestAutoKeyProviderListExcludesMasterKey(t *testing.T) {
	p := setupAutoKeyProvider(t)
	defer p.Close()

	p.Set("a/one", "v1")
	p.Set("b/two", "v2")

	keys, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("List returned %d keys, want 2: %v", len(keys), keys)
	}
	for _, k := range keys {
		if k == ".master.key" {
			t.Error("List should exclude .master.key")
		}
	}
}

func TestLocalProviderInterfaceSatisfied(t *testing.T) {
	var _ Provider = (*LocalProvider)(nil)
}

func TestEnsureTLSCertsGenerates(t *testing.T) {
	tlsDir := t.TempDir()

	if err := EnsureTLSCerts(tlsDir); err != nil {
		t.Fatalf("EnsureTLSCerts: %v", err)
	}

	// All four PEM files should exist on disk.
	caPath, caKeyPath, certPath, keyPath := TLSCertPaths(tlsDir)
	for _, f := range []string{caPath, caKeyPath, certPath, keyPath} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("ReadFile %q: %v", f, err)
		}
		if !strings.Contains(string(data), "-----BEGIN") {
			t.Errorf("%q does not look like PEM", f)
		}
	}
}

func TestEnsureTLSCertsIdempotent(t *testing.T) {
	tlsDir := t.TempDir()

	if err := EnsureTLSCerts(tlsDir); err != nil {
		t.Fatalf("first call: %v", err)
	}

	caPath, _, _, _ := TLSCertPaths(tlsDir)
	first, _ := os.ReadFile(caPath)

	// Second call should be a no-op (files already exist).
	if err := EnsureTLSCerts(tlsDir); err != nil {
		t.Fatalf("second call: %v", err)
	}

	second, _ := os.ReadFile(caPath)
	if string(first) != string(second) {
		t.Error("EnsureTLSCerts regenerated existing certs — should be idempotent")
	}
}

func TestEnsureTLSCertsExtraSANs(t *testing.T) {
	tlsDir := t.TempDir()

	if err := EnsureTLSCerts(tlsDir, "192.168.1.2", "vigo.example.com"); err != nil {
		t.Fatalf("EnsureTLSCerts: %v", err)
	}

	_, _, certPath, _ := TLSCertPaths(tlsDir)
	data, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	block, _ := pem.Decode(data)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	// Check that the extra IP is present.
	foundIP := false
	for _, ip := range cert.IPAddresses {
		if ip.String() == "192.168.1.2" {
			foundIP = true
		}
	}
	if !foundIP {
		t.Errorf("cert IPAddresses %v missing 192.168.1.2", cert.IPAddresses)
	}

	// Check that the extra DNS name is present.
	foundDNS := false
	for _, name := range cert.DNSNames {
		if name == "vigo.example.com" {
			foundDNS = true
		}
	}
	if !foundDNS {
		t.Errorf("cert DNSNames %v missing vigo.example.com", cert.DNSNames)
	}
}

// TestEnsureTLSCertsRefreshesOnSANDrift verifies that adding a new SAN to
// the expected set triggers a server-cert refresh, while leaving the CA
// untouched so existing enrollments keep their trust chain. Uses DNS names
// in the TEST-NET / RFC2606 documentation ranges so the test is independent
// of the machine's actual network configuration.
func TestEnsureTLSCertsRefreshesOnSANDrift(t *testing.T) {
	tlsDir := t.TempDir()

	if err := EnsureTLSCerts(tlsDir, "vigo-old.example.test"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	caPath, caKeyPath, certPath, _ := TLSCertPaths(tlsDir)
	caBefore, _ := os.ReadFile(caPath)
	caKeyBefore, _ := os.ReadFile(caKeyPath)
	certBefore, _ := os.ReadFile(certPath)

	if err := EnsureTLSCerts(tlsDir, "vigo-new.example.test"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	caAfter, _ := os.ReadFile(caPath)
	caKeyAfter, _ := os.ReadFile(caKeyPath)
	certAfter, _ := os.ReadFile(certPath)

	if !bytes.Equal(caBefore, caAfter) {
		t.Error("CA cert changed across SAN drift — should preserve the CA")
	}
	if !bytes.Equal(caKeyBefore, caKeyAfter) {
		t.Error("CA key changed across SAN drift — should preserve the CA key")
	}
	if bytes.Equal(certBefore, certAfter) {
		t.Error("server cert unchanged across SAN drift — should have been refreshed")
	}

	block, _ := pem.Decode(certAfter)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	found := false
	for _, name := range cert.DNSNames {
		if name == "vigo-new.example.test" {
			found = true
		}
	}
	if !found {
		t.Errorf("refreshed cert DNSNames %v missing vigo-new.example.test", cert.DNSNames)
	}

	// The refreshed server cert must verify against the original CA.
	caBlock, _ := pem.Decode(caBefore)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(CA): %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:       roots,
		DNSName:     "vigo-new.example.test",
		CurrentTime: time.Now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("refreshed cert not signed by original CA: %v", err)
	}
}

// TestEnsureTLSCertsNoRefreshWhenCovered verifies that shrinking the expected
// SAN set is a no-op — the on-disk cert is a superset of what's needed.
func TestEnsureTLSCertsNoRefreshWhenCovered(t *testing.T) {
	tlsDir := t.TempDir()

	if err := EnsureTLSCerts(tlsDir, "vigo-a.example.test", "vigo-b.example.test"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, _, certPath, _ := TLSCertPaths(tlsDir)
	before, _ := os.ReadFile(certPath)

	if err := EnsureTLSCerts(tlsDir, "vigo-a.example.test"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	after, _ := os.ReadFile(certPath)

	if !bytes.Equal(before, after) {
		t.Error("server cert regenerated when expected SANs were already covered — should be a no-op")
	}
}

func TestDeriveKeyHex(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	hexStr := hex.EncodeToString(raw) + "\n"

	key, err := deriveKey([]byte(hexStr))
	if err != nil {
		t.Fatalf("deriveKey: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}
	for i := range raw {
		if key[i] != raw[i] {
			t.Errorf("key[%d] = %d, want %d", i, key[i], raw[i])
			break
		}
	}
}

func TestDeriveKeyRawBytes(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 100)
	}

	key, err := deriveKey(raw)
	if err != nil {
		t.Fatalf("deriveKey: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}
	for i := range raw {
		if key[i] != raw[i] {
			t.Errorf("key[%d] = %d, want %d", i, key[i], raw[i])
			break
		}
	}
}

func TestDeriveKeyPassphrase(t *testing.T) {
	// Arbitrary-length input gets SHA-256 hashed.
	key, err := deriveKey([]byte("my-secret-passphrase"))
	if err != nil {
		t.Fatalf("deriveKey: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}
}

func TestLocalProviderSymlinkEscapeGet(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Create a file outside the secrets directory.
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "stolen")
	os.WriteFile(outsideFile, []byte("sensitive data"), 0o600)

	// Create a symlink inside the secrets directory pointing outside.
	symlinkPath := filepath.Join(p.secretsDir, "evil")
	if err := os.Symlink(outsideFile, symlinkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_, err := p.Get(context.Background(), "evil")
	if err == nil {
		t.Fatal("expected error for symlink escaping secrets directory, got nil")
	}
	if !strings.Contains(err.Error(), "resolves outside secrets directory") {
		t.Errorf("error = %q, want it to contain %q", err, "resolves outside secrets directory")
	}
}

func TestLocalProviderSymlinkEscapeSet(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Create a directory outside the secrets dir to be the symlink target.
	outsideDir := t.TempDir()

	// Create a symlink inside the secrets directory whose parent points outside.
	// E.g., secrets/evil -> outsideDir, then Set("evil/key", ...) would write outside.
	symlinkPath := filepath.Join(p.secretsDir, "evil")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	err := p.Set("evil/key", "malicious data")
	if err == nil {
		t.Fatal("expected error for symlink escaping secrets directory on Set, got nil")
	}
	if !strings.Contains(err.Error(), "resolves outside secrets directory") {
		t.Errorf("error = %q, want it to contain %q", err, "resolves outside secrets directory")
	}

	// Verify no file was written outside.
	if _, err := os.Stat(filepath.Join(outsideDir, "key")); err == nil {
		t.Error("file was written outside secrets directory via symlink")
	}
}
