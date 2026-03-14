// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package secrets

import (
	"fmt"
	"log/slog"
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
	TokenFile     string `yaml:"token_file"`      // isopass: path to file containing bearer token
	TokenEnv      string `yaml:"token_env"`       // isopass: DEPRECATED — env var containing bearer token (use token_file instead)
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
		token, err := loadIsopassToken(cfg)
		if err != nil {
			return nil, fmt.Errorf("secrets backend %q: %w", backend, err)
		}
		return NewIsopassProvider(cfg.URL, token, cfg.TLSSkipVerify)
	default:
		return nil, fmt.Errorf("unknown secrets backend %q (expected \"local\" or \"isopass\")", backend)
	}
}

// loadIsopassToken resolves the bearer token for the isopass backend.
// Priority: token_file (recommended) > token_env (deprecated fallback).
func loadIsopassToken(cfg Config) (string, error) {
	// Preferred: read from file (no secrets in env vars).
	if cfg.TokenFile != "" {
		data, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			return "", fmt.Errorf("reading token_file %q: %w", cfg.TokenFile, err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("token_file %q is empty", cfg.TokenFile)
		}
		return token, nil
	}

	// Deprecated fallback: read from environment variable.
	tokenEnv := cfg.TokenEnv
	if tokenEnv == "" {
		tokenEnv = "HOBNOB_SECRETS_TOKEN"
	}
	token := os.Getenv(tokenEnv)
	if token != "" {
		slog.Warn("isopass token loaded from environment variable — migrate to token_file for security",
			"env", tokenEnv)
		return token, nil
	}

	return "", fmt.Errorf("no token configured — set token_file in server.yaml (recommended) or env var %q (deprecated)", tokenEnv)
}
