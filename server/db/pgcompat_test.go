// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package db

import "testing"

func TestRewritePlaceholders(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"SELECT * FROM t WHERE id = ?", "SELECT * FROM t WHERE id = $1"},
		{"INSERT INTO t (a, b) VALUES (?, ?)", "INSERT INTO t (a, b) VALUES ($1, $2)"},
		{"SELECT * FROM t", "SELECT * FROM t"},
		{"DELETE FROM t WHERE a = ? AND b = ? AND c = ?", "DELETE FROM t WHERE a = $1 AND b = $2 AND c = $3"},
		{"SELECT * FROM t WHERE name = 'what?' AND id = ?", "SELECT * FROM t WHERE name = 'what?' AND id = $1"},
		{"", ""},
		{"SELECT '?', ?", "SELECT '?', $1"},
	}

	for _, tt := range tests {
		got := rewritePlaceholders(tt.input)
		if got != tt.want {
			t.Errorf("rewritePlaceholders(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
