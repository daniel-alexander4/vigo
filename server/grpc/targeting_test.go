// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"testing"
)

func TestJsonPointerLookup(t *testing.T) {
	data := map[string]any{
		"os": map[string]any{
			"distro": "ubuntu",
			"family": "debian",
		},
		"hostname": "web01.prod",
	}

	tests := []struct {
		pointer string
		want    any
	}{
		{"/os/distro", "ubuntu"},
		{"/os/family", "debian"},
		{"/hostname", "web01.prod"},
		{"/os/nonexistent", nil},
		{"/nonexistent", nil},
		{"/os/distro/deep", nil},
	}

	for _, tt := range tests {
		got := jsonPointerLookup(data, tt.pointer)
		if got != tt.want {
			t.Errorf("jsonPointerLookup(%q) = %v, want %v", tt.pointer, got, tt.want)
		}
	}
}

func TestJsonPointerLookupNumeric(t *testing.T) {
	data := map[string]any{
		"memory": map[string]any{
			"total_mb": float64(8192),
		},
	}

	got := jsonPointerLookup(data, "/memory/total_mb")
	if got != float64(8192) {
		t.Errorf("expected 8192, got %v", got)
	}
}
