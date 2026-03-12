// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package config

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hobnob/hobnob/server/secrets"
	"gopkg.in/yaml.v3"
)

// LoadSecretsConfig reads just the secrets section from server.yaml.
// This is called before the secrets provider exists, so no secret: resolution
// is performed. Returns the secrets.Config needed to create a provider.
func LoadSecretsConfig(path string) (secrets.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return secrets.Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}

	var raw struct {
		Secrets secrets.Config `yaml:"secrets"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return secrets.Config{}, fmt.Errorf("parsing config: %w", err)
	}

	return raw.Secrets, nil
}

// LoadServerConfig reads server.yaml from the given path and resolves any
// secret: prefixed values using the provided secrets provider.
func LoadServerConfig(path string, provider secrets.Provider) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading server config %s: %w", path, err)
	}

	var cfg ServerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing server config: %w", err)
	}

	// Resolve secret: prefixes in config values.
	if err := resolveServerSecrets(&cfg, provider); err != nil {
		return nil, fmt.Errorf("resolving server config secrets: %w", err)
	}

	return &cfg, nil
}

// resolveServerSecrets resolves secret: prefixed values in the server config.
// Collects all failures so the operator can fix everything in one restart.
func resolveServerSecrets(cfg *ServerConfig, provider secrets.Provider) error {
	type field struct {
		ptr  *string
		name string
	}
	fields := []field{
		{&cfg.Database.DSN, "database.dsn"},
		{&cfg.Server.TLS.CertFile, "server.tls.cert_file"},
		{&cfg.Server.TLS.KeyFile, "server.tls.key_file"},
		{&cfg.Server.TLS.CAFile, "server.tls.ca_file"},
		{&cfg.Server.TLS.CAKeyFile, "server.tls.ca_key_file"},
	}

	var errs []*SecretError
	for _, f := range fields {
		if strings.HasPrefix(*f.ptr, secretPrefix) {
			key := strings.TrimPrefix(*f.ptr, secretPrefix)
			val, err := provider.Get(context.Background(), key)
			if err != nil {
				errs = append(errs, &SecretError{
					Match:  "server.yaml",
					Attr:   f.name,
					Secret: key,
					Err:    err,
				})
				continue
			}
			*f.ptr = val
		}
	}
	if len(errs) > 0 {
		return &SecretErrors{Errors: errs}
	}

	return nil
}
