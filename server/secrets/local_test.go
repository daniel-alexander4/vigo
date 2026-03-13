// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package secrets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	if err := p.Set("hobnob/db/dsn", "my-database.db"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "hobnob/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "my-database.db" {
		t.Errorf("Get = %q, want %q", val, "my-database.db")
	}
}

func TestLocalProviderGetBytes(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	if err := p.Set("hobnob/tls/cert", "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.GetBytes(context.Background(), "hobnob/tls/cert")
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
	val, err := p.Get(context.Background(), "hobnob/db/dsn")
	if err != nil {
		t.Fatalf("Get builtin: %v", err)
	}
	if val != "/etc/hobnob-server/db/hobnob.db" {
		t.Errorf("builtin fallback = %q, want %q", val, "/etc/hobnob-server/db/hobnob.db")
	}
}

func TestLocalProviderOverridesBuiltin(t *testing.T) {
	p, _ := setupLocalProvider(t)
	defer p.Close()

	// Stored value overrides built-in default.
	if err := p.Set("hobnob/db/dsn", "custom.db"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "hobnob/db/dsn")
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

// --- Plaintext mode tests (no key_file) ---

func setupPlaintextProvider(t *testing.T) *LocalProvider {
	t.Helper()
	secretsDir := filepath.Join(t.TempDir(), "secrets")
	p, err := NewLocalProvider("", secretsDir)
	if err != nil {
		t.Fatalf("NewLocalProvider plaintext: %v", err)
	}
	return p
}

func TestPlaintextProviderSetAndGet(t *testing.T) {
	p := setupPlaintextProvider(t)
	defer p.Close()

	if err := p.Set("hobnob/db/dsn", "my-database.db"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get(context.Background(), "hobnob/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "my-database.db" {
		t.Errorf("Get = %q, want %q", val, "my-database.db")
	}
}

func TestPlaintextProviderFileIsReadable(t *testing.T) {
	p := setupPlaintextProvider(t)
	defer p.Close()

	if err := p.Set("test/secret", "hello-world"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The file should be plaintext — readable without decryption.
	path := filepath.Join(p.secretsDir, "test", "secret")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello-world" {
		t.Errorf("file content = %q, want %q", string(data), "hello-world")
	}
}

func TestPlaintextProviderManualFile(t *testing.T) {
	p := setupPlaintextProvider(t)
	defer p.Close()

	// Manually write a secret file (simulating user creating it).
	dir := filepath.Join(p.secretsDir, "hobnob", "db")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "dsn"), []byte("manual-secret\n"), 0o600)

	val, err := p.Get(context.Background(), "hobnob/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "manual-secret" {
		t.Errorf("Get = %q, want %q (trailing newline should be trimmed)", val, "manual-secret")
	}
}

func TestPlaintextProviderBuiltinFallback(t *testing.T) {
	p := setupPlaintextProvider(t)
	defer p.Close()

	val, err := p.Get(context.Background(), "hobnob/db/dsn")
	if err != nil {
		t.Fatalf("Get builtin: %v", err)
	}
	if val != "/etc/hobnob-server/db/hobnob.db" {
		t.Errorf("builtin fallback = %q, want %q", val, "/etc/hobnob-server/db/hobnob.db")
	}
}

func TestPlaintextProviderDelete(t *testing.T) {
	p := setupPlaintextProvider(t)
	defer p.Close()

	p.Set("temp/key", "value")
	if err := p.Delete("temp/key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := p.Get(context.Background(), "temp/key")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestPlaintextProviderList(t *testing.T) {
	p := setupPlaintextProvider(t)
	defer p.Close()

	p.Set("a/one", "v1")
	p.Set("b/two", "v2")

	keys, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("List returned %d keys, want 2", len(keys))
	}
}

func TestLocalProviderInterfaceSatisfied(t *testing.T) {
	var _ Provider = (*LocalProvider)(nil)
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
