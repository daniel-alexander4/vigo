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
	Backend       string `yaml:"backend"`         // "local" (default) or "isopass"
	KeyFile       string `yaml:"key_file"`        // local: path to master encryption key (empty = auto-generate)
	SecretsDir    string `yaml:"secrets_dir"`     // local: directory for secret files (default: /etc/hobnob-server/secrets)
	URL           string `yaml:"url"`             // isopass: API base URL
	TokenFile     string `yaml:"token_file"`      // isopass: path to file containing bearer token (0600)
	TLSSkipVerify bool   `yaml:"tls_skip_verify"` // isopass: skip TLS certificate verification
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
			secretsDir = "/etc/hobnob-server/secrets"
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
		return nil, fmt.Errorf("unknown secrets backend %q (expected \"local\" or \"isopass\")", backend)
	}
}
