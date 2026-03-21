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
	Backend       string `yaml:"backend"`         // "local" (default), "isopass", "vault", "aws", "azure", "gcp"
	KeyFile       string `yaml:"key_file"`        // local: path to master encryption key (empty = auto-generate)
	SecretsDir    string `yaml:"secrets_dir"`     // local: directory for secret files (default: /srv/vigo/secrets)
	URL           string `yaml:"url"`             // isopass/vault: API base URL
	TokenFile     string `yaml:"token_file"`      // isopass/vault: path to file containing bearer token (0600)
	TLSSkipVerify bool   `yaml:"tls_skip_verify"` // isopass/vault: skip TLS certificate verification
	MountPath     string `yaml:"mount_path"`      // vault: KV engine mount path (default: "secret")
	Region        string `yaml:"region"`          // aws: AWS region
	ProjectID     string `yaml:"project_id"`      // gcp: GCP project ID
	VaultName     string `yaml:"vault_name"`      // azure: Key Vault name
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
	case "vault":
		if cfg.URL == "" {
			return nil, fmt.Errorf("secrets backend %q requires url", backend)
		}
		mount := cfg.MountPath
		if mount == "" {
			mount = "secret"
		}
		token := ""
		if cfg.TokenFile != "" {
			data, err := os.ReadFile(cfg.TokenFile)
			if err != nil {
				return nil, fmt.Errorf("secrets backend %q: reading token_file: %w", backend, err)
			}
			token = strings.TrimSpace(string(data))
		}
		return NewVaultProvider(cfg.URL, token, mount, cfg.TLSSkipVerify)
	case "aws":
		region := cfg.Region
		if region == "" {
			region = "us-east-1"
		}
		return NewAWSProvider(region)
	case "azure":
		if cfg.VaultName == "" {
			return nil, fmt.Errorf("secrets backend %q requires vault_name", backend)
		}
		return NewAzureProvider(cfg.VaultName)
	case "gcp":
		if cfg.ProjectID == "" {
			return nil, fmt.Errorf("secrets backend %q requires project_id", backend)
		}
		return NewGCPProvider(cfg.ProjectID)
	default:
		return nil, fmt.Errorf("unknown secrets backend %q (expected local, isopass, vault, aws, azure, or gcp)", backend)
	}
}
