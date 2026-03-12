// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package secrets

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestStaticProviderGet(t *testing.T) {
	p, err := NewStaticProvider("testdata/.env.test")
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
	defer p.Close()

	val, err := p.Get(context.Background(), "hobnob/db/dsn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "hobnob_test.db" {
		t.Errorf("got %q, want SQLite DSN", val)
	}
}

func TestStaticProviderGetBytes(t *testing.T) {
	p, err := NewStaticProvider("testdata/.env.test")
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
	defer p.Close()

	val, err := p.GetBytes(context.Background(), "hobnob/db/dsn")
	if err != nil {
		t.Fatalf("GetBytes: %v", err)
	}
	expected := "hobnob_test.db"
	if string(val) != expected {
		t.Errorf("got %q, want %q", string(val), expected)
	}
}

func TestStaticProviderMissing(t *testing.T) {
	p, err := NewStaticProvider("testdata/.env.test")
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
	defer p.Close()

	_, err = p.Get(context.Background(), "nonexistent/key/path")
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
}

func TestSecretStringRedaction(t *testing.T) {
	s := NewSecretString("super-secret-value")
	if s.String() != "[REDACTED]" {
		t.Errorf("String() = %q, want [REDACTED]", s.String())
	}
}

func TestSecretStringValue(t *testing.T) {
	s := NewSecretString("super-secret-value")
	if s.Value() != "super-secret-value" {
		t.Errorf("Value() = %q, want super-secret-value", s.Value())
	}
}

func TestSecretStringJSON(t *testing.T) {
	s := NewSecretString("super-secret-value")
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(b) != `"[REDACTED]"` {
		t.Errorf("json.Marshal = %s, want \"[REDACTED]\"", string(b))
	}
}

func TestSecretStringSlog(t *testing.T) {
	s := NewSecretString("super-secret-value")
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("test", "secret", s)
	output := buf.String()
	if strings.Contains(output, "super-secret-value") {
		t.Errorf("slog output contains secret value: %s", output)
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Errorf("slog output missing [REDACTED]: %s", output)
	}
}

func TestProviderInterfaceSatisfied(t *testing.T) {
	var _ Provider = (*StaticProvider)(nil)
}
