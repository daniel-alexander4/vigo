// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package secrets

import (
	"fmt"
	"testing"
)

func TestSecretStringMarshalText(t *testing.T) {
	s := NewSecretString("super-secret-value")
	b, err := s.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	if string(b) != "[REDACTED]" {
		t.Errorf("MarshalText = %q, want [REDACTED]", string(b))
	}
}

func TestSecretStringSprintf(t *testing.T) {
	s := NewSecretString("super-secret-value")
	got := s.String()
	if got != "[REDACTED]" {
		t.Errorf("String() = %q, want [REDACTED]", got)
	}
	got = fmt.Sprintf("%v", s)
	if got != "[REDACTED]" {
		t.Errorf("Sprintf %%v = %q, want [REDACTED]", got)
	}
}

func TestSecretStringZeroValue(t *testing.T) {
	var s SecretString
	if s.String() != "[REDACTED]" {
		t.Errorf("zero-value String() = %q, want [REDACTED]", s.String())
	}
	if s.Value() != "" {
		t.Errorf("zero-value Value() = %q, want empty string", s.Value())
	}
	if got := s.LogValue().String(); got != "[REDACTED]" {
		t.Errorf("zero-value LogValue() = %v, want [REDACTED]", got)
	}
}
