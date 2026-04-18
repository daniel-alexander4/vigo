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
	Backend       string `yaml:"backend" doc:"Secrets provider backend." options:"local|isopass|vault|aws|azure|gcp" default:"local" example:"local"`
	KeyFile       string `yaml:"key_file" doc:"Local backend: path to the master encryption key file. Empty means auto-generate." example:"/srv/vigo/secrets/.master-key"`
	SecretsDir    string `yaml:"secrets_dir" doc:"Local backend: directory holding secret files." default:"/srv/vigo/secrets" example:"/srv/vigo/secrets"`
	URL           string `yaml:"url" doc:"Isopass / Vault: provider API base URL." example:"https://vault.example.com:8200"`
	TokenFile     string `yaml:"token_file" doc:"Isopass / Vault: path to a 0600 file containing the bearer token." example:"/etc/vigo/vault-token"`
	TLSSkipVerify bool   `yaml:"tls_skip_verify" doc:"Isopass / Vault: skip TLS certificate verification (development only)." default:"false" example:"dev only — never in prod"`
	MountPath     string `yaml:"mount_path" doc:"Vault: KV engine mount path." default:"secret" example:"kv, secret, vigo-kv"`
	Region        string `yaml:"region" doc:"AWS: region for Secrets Manager." example:"us-west-2"`
	ProjectID     string `yaml:"project_id" doc:"GCP: project ID for Secret Manager." example:"my-gcp-project"`
	VaultName     string `yaml:"vault_name" doc:"Azure: Key Vault name." example:"vigo-prod-vault"`
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
