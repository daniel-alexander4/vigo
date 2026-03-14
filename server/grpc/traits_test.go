// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRejectOversizedTraits(t *testing.T) {
	// Build a traits payload with 501 keys.
	traits := make(map[string]string, 501)
	for i := 0; i < 501; i++ {
		traits[strings.Repeat("k", 10)+string(rune('a'+i%26))+strings.Repeat("x", i)] = "v"
	}
	data, _ := json.Marshal(traits)
	err := validateTraitsSize(data)
	if err == nil {
		t.Fatal("expected error for 501 traits, got nil")
	}
	if !strings.Contains(err.Error(), "too many traits") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRejectOversizedTraitValue(t *testing.T) {
	// Build a traits payload with one value > 64 KiB.
	bigValue := strings.Repeat("x", 65*1024)
	traits := map[string]string{"big": bigValue}
	data, _ := json.Marshal(traits)
	err := validateTraitsSize(data)
	if err == nil {
		t.Fatal("expected error for oversized trait value, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAcceptNormalTraits(t *testing.T) {
	traits := map[string]string{
		"os":       "linux",
		"hostname": "web-01.example.com",
		"arch":     "amd64",
	}
	data, _ := json.Marshal(traits)
	if err := validateTraitsSize(data); err != nil {
		t.Fatalf("normal traits should be accepted: %v", err)
	}
}
