// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package secrets

import (
	"fmt"
	"os"
	"strings"
)

// Config describes which secrets backend to use and its settings.
// Values in this struct must not use "secret:" prefixes — this config
// is loaded before the provider exists.
type Config struct {
	Backend       string `yaml:"backend" doc:"Secrets provider backend." options:"local|isopass" default:"local" example:"local"`
	KeyFile       string `yaml:"key_file" doc:"Local backend: path to the master encryption key file. Empty means auto-generate." example:"/srv/vigo/secrets/.master-key"`
	SecretsDir    string `yaml:"secrets_dir" doc:"Local backend: directory holding secret files." default:"/srv/vigo/secrets" example:"/srv/vigo/secrets"`
	URL           string `yaml:"url" doc:"Isopass: provider API base URL." example:"https://isopass.example.com:8443"`
	TokenFile     string `yaml:"token_file" doc:"Isopass: path to a 0600 file containing the bearer token." example:"/etc/vigo/isopass-token"`
	TLSSkipVerify bool   `yaml:"tls_skip_verify" doc:"Isopass: skip TLS certificate verification (development only)." default:"false" example:"dev only — never in prod"`
}

// NewFromConfig creates a Provider based on the given config.
// If Backend is empty, it defaults to "local" (auto-generates encryption key if none specified).
func NewFromConfig(cfg Config) (Provider, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = "local"
	}

	switch backend {
	case "local":
		secretsDir := cfg.SecretsDir
		if secretsDir == "" {
			secretsDir = "/srv/vigo/secrets"
		}
		return NewLocalProvider(cfg.KeyFile, secretsDir)
	case "isopass":
		if cfg.URL == "" {
			return nil, fmt.Errorf("secrets backend %q requires url", backend)
		}
		if cfg.TokenFile == "" {
			return nil, fmt.Errorf("secrets backend %q requires token_file", backend)
		}
		data, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("secrets backend %q: reading token_file %q: %w", backend, cfg.TokenFile, err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return nil, fmt.Errorf("secrets backend %q: token_file %q is empty", backend, cfg.TokenFile)
		}
		return NewIsopassProvider(cfg.URL, token, cfg.TLSSkipVerify)
	default:
		return nil, fmt.Errorf("unknown secrets backend %q (expected local or isopass)", backend)
	}
}
