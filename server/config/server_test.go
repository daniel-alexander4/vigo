// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadServerCertSANs(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{
			name: "hostname and tls_sans",
			yaml: "server:\n  hostname: danlap\n  tls_sans:\n    - 192.168.1.2\n    - vigo.example.com\n",
			want: []string{"192.168.1.2", "vigo.example.com", "danlap"},
		},
		{
			name: "hostname only",
			yaml: "server:\n  hostname: danlap\n",
			want: []string{"danlap"},
		},
		{
			name: "tls_sans only",
			yaml: "server:\n  tls_sans:\n    - vigo.example.com\n",
			want: []string{"vigo.example.com"},
		},
		{
			name: "empty hostname skipped",
			yaml: "server:\n  hostname: \"\"\n  tls_sans:\n    - vigo.example.com\n",
			want: []string{"vigo.example.com"},
		},
		{
			name: "localhost hostname skipped",
			yaml: "server:\n  hostname: localhost\n",
			want: nil,
		},
		{
			name: "no server block",
			yaml: "database:\n  driver: sqlite\n",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0644); err != nil {
				t.Fatal(err)
			}
			if got := LoadServerCertSANs(path); !slices.Equal(got, tt.want) {
				t.Errorf("LoadServerCertSANs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadServerCertSANsMissingFile(t *testing.T) {
	if got := LoadServerCertSANs(filepath.Join(t.TempDir(), "nonexistent.yaml")); got != nil {
		t.Errorf("LoadServerCertSANs(missing) = %v, want nil", got)
	}
}
