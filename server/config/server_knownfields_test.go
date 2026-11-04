// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type anySecretProvider struct{}

func (anySecretProvider) Get(context.Context, string) (string, error)      { return "x", nil }
func (anySecretProvider) GetBytes(context.Context, string) ([]byte, error) { return []byte("x"), nil }
func (anySecretProvider) Close() error                                     { return nil }

// TestShippedServerYAMLsLoad: the example and the seed server.yaml load
// under the strict decoder.
func TestShippedServerYAMLsLoad(t *testing.T) {
	for _, p := range []string{"server.yaml.example", filepath.Join("..", "..", "server-seed", "server.yaml")} {
		if _, err := LoadServerConfig(p, anySecretProvider{}, "local"); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// TestServerYAMLUnknownKeyRejected: a misspelled or removed key is an
// error naming the key, not silently dropped.
func TestServerYAMLUnknownKeyRejected(t *testing.T) {
	for _, body := range []string{
		"license:\n  key: abc\n",           // removed block
		"server:\n  hostnme: vigo.local\n", // typo
	} {
		path := filepath.Join(t.TempDir(), "server.yaml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadServerConfig(path, anySecretProvider{}, "local")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("%q: err = %v, want an unknown-field error", body, err)
		}
	}
}
