// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import "testing"

func TestParseMemoryLimit(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"invalid", 0},
		{"1024", 1024},
		{"100B", 100},
		{"512KiB", 512 * 1024},
		{"256MiB", 256 * 1024 * 1024},
		{"4GiB", 4 * 1024 * 1024 * 1024},
		{"1TiB", 1 << 40},
		{"0", 0},
		{"-1", 0},
	}

	for _, tt := range tests {
		got := parseMemoryLimit(tt.input)
		if got != tt.want {
			t.Errorf("parseMemoryLimit(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
