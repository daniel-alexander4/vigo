// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

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
	val, err := p.Get(t.Context(), "vigo/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "/srv/vigo/db/vigo.db" {
		t.Errorf("got %q, want %q", val, "/srv/vigo/db/vigo.db")
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
	val, err := p.Get(t.Context(), "vigo/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "/srv/vigo/db/vigo.db" {
		t.Errorf("got %q, want %q", val, "/srv/vigo/db/vigo.db")
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

	// Default secrets_dir is /srv/vigo/secrets. Use explicit dir in test.
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

func TestNewFromConfigIsopassMissingTokenFile(t *testing.T) {
	_, err := NewFromConfig(Config{
		Backend: "isopass",
		URL:     "https://isopass.internal:8443",
	})
	if err == nil {
		t.Fatal("expected error for missing token_file")
	}
}

func TestNewFromConfigIsopassWithTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	os.WriteFile(tokenPath, []byte("file-bearer-token\n"), 0o600)

	p, err := NewFromConfig(Config{
		Backend:   "isopass",
		URL:       "https://isopass.internal:8443",
		TokenFile: tokenPath,
	})
	if err != nil {
		t.Fatalf("NewFromConfig isopass: %v", err)
	}
	defer p.Close()
}

func TestNewFromConfigIsopassEmptyTokenFileErrors(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	os.WriteFile(tokenPath, []byte("  \n"), 0o600)

	_, err := NewFromConfig(Config{
		Backend:   "isopass",
		URL:       "https://isopass.internal:8443",
		TokenFile: tokenPath,
	})
	if err == nil {
		t.Fatal("expected error for empty token file")
	}
}

func TestNewFromConfigIsopassMissingTokenFileErrors(t *testing.T) {
	_, err := NewFromConfig(Config{
		Backend:   "isopass",
		URL:       "https://isopass.internal:8443",
		TokenFile: "/nonexistent/path/token",
	})
	if err == nil {
		t.Fatal("expected error for missing token file")
	}
}
