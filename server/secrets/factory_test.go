// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewFromConfigStatic(t *testing.T) {
	p, err := NewFromConfig(Config{
		Backend: "static",
		EnvFile: "testdata/.env.test",
	})
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	defer p.Close()

	val, err := p.Get(t.Context(), "hobnob/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "hobnob_test.db" {
		t.Errorf("got %q", val)
	}
}

func TestNewFromConfigDefaultsToLocal(t *testing.T) {
	dir := t.TempDir()
	p, err := NewFromConfig(Config{
		SecretsDir: filepath.Join(dir, "secrets"),
	})
	if err != nil {
		t.Fatalf("NewFromConfig with empty backend: %v", err)
	}
	defer p.Close()

	// Default backend is local — should resolve builtin defaults.
	val, err := p.Get(t.Context(), "hobnob/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "/etc/hobnob-server/db/hobnob.db" {
		t.Errorf("got %q, want %q", val, "/etc/hobnob-server/db/hobnob.db")
	}
}

func TestNewFromConfigLocal(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")
	os.WriteFile(keyFile, []byte("0000000000000000000000000000000000000000000000000000000000000000\n"), 0o600)

	p, err := NewFromConfig(Config{
		Backend:    "local",
		KeyFile:    keyFile,
		SecretsDir: filepath.Join(dir, "secrets"),
	})
	if err != nil {
		t.Fatalf("NewFromConfig local: %v", err)
	}
	defer p.Close()

	// Should resolve built-in default.
	val, err := p.Get(t.Context(), "hobnob/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "/etc/hobnob-server/db/hobnob.db" {
		t.Errorf("got %q, want %q", val, "/etc/hobnob-server/db/hobnob.db")
	}
}

func TestNewFromConfigLocalAutoKey(t *testing.T) {
	// No key_file = auto-generated key (always encrypted).
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	p, err := NewFromConfig(Config{
		Backend:    "local",
		SecretsDir: secretsDir,
	})
	if err != nil {
		t.Fatalf("NewFromConfig local auto-key: %v", err)
	}
	defer p.Close()

	// Master key should have been created.
	if _, err := os.Stat(filepath.Join(secretsDir, ".master.key")); err != nil {
		t.Fatalf("auto-generated master key not found: %v", err)
	}
}

func TestNewFromConfigLocalDefaultSecretsDir(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")
	os.WriteFile(keyFile, []byte("0000000000000000000000000000000000000000000000000000000000000000\n"), 0o600)

	// Default secrets_dir is /etc/hobnob-server/secrets. Use explicit dir in test.
	p, err := NewFromConfig(Config{
		Backend:    "local",
		KeyFile:    keyFile,
		SecretsDir: filepath.Join(dir, "secrets"),
	})
	if err != nil {
		t.Fatalf("NewFromConfig local default dir: %v", err)
	}
	defer p.Close()
}

func TestNewFromConfigUnknownBackend(t *testing.T) {
	_, err := NewFromConfig(Config{Backend: "vault"})
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func TestNewFromConfigIsopassMissingURL(t *testing.T) {
	_, err := NewFromConfig(Config{Backend: "isopass"})
	if err == nil {
		t.Fatal("expected error for missing isopass URL")
	}
}

func TestNewFromConfigIsopassMissingToken(t *testing.T) {
	_, err := NewFromConfig(Config{
		Backend: "isopass",
		URL:     "https://isopass.internal:8443",
	})
	if err == nil {
		t.Fatal("expected error for missing isopass token")
	}
}

func TestNewFromConfigIsopassWithToken(t *testing.T) {
	t.Setenv("HOBNOB_SECRETS_TOKEN", "test-bearer-token")
	p, err := NewFromConfig(Config{
		Backend: "isopass",
		URL:     "https://isopass.internal:8443",
	})
	if err != nil {
		t.Fatalf("NewFromConfig isopass: %v", err)
	}
	defer p.Close()
}
