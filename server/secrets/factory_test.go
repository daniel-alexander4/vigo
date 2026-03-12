// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package secrets

import (
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

func TestNewFromConfigDefaultsToStatic(t *testing.T) {
	p, err := NewFromConfig(Config{
		EnvFile: "testdata/.env.test",
	})
	if err != nil {
		t.Fatalf("NewFromConfig with empty backend: %v", err)
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
