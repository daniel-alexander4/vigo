// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package secrets

import (
	"fmt"
	"os"
)

// Config describes which secrets backend to use and its settings.
// Values in this struct must not use "secret:" prefixes — this config
// is loaded before the provider exists.
type Config struct {
	Backend       string `yaml:"backend"`         // "local" (default), "static", or "isopass"
	EnvFile       string `yaml:"env_file"`        // static: path to .env file
	KeyFile       string `yaml:"key_file"`        // local: path to master encryption key (empty = plaintext)
	SecretsDir    string `yaml:"secrets_dir"`     // local: directory for secret files (default: /etc/hobnob-server/secrets)
	URL           string `yaml:"url"`             // isopass: API base URL
	TokenEnv      string `yaml:"token_env"`       // isopass: env var containing bearer token
	TLSSkipVerify bool   `yaml:"tls_skip_verify"` // isopass: skip TLS certificate verification
}

// NewFromConfig creates a Provider based on the given config.
// If Backend is empty, it defaults to "local" (plaintext mode, no encryption key required).
func NewFromConfig(cfg Config) (Provider, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = "local"
	}

	switch backend {
	case "static":
		envFile := cfg.EnvFile
		if envFile == "" {
			envFile = ".env"
		}
		return NewStaticProvider(envFile)
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
		tokenEnv := cfg.TokenEnv
		if tokenEnv == "" {
			tokenEnv = "HOBNOB_SECRETS_TOKEN"
		}
		token := os.Getenv(tokenEnv)
		if token == "" {
			return nil, fmt.Errorf("secrets backend %q: env var %q is empty", backend, tokenEnv)
		}
		return NewIsopassProvider(cfg.URL, token, cfg.TLSSkipVerify)
	default:
		return nil, fmt.Errorf("unknown secrets backend %q (expected \"static\", \"local\", or \"isopass\")", backend)
	}
}
