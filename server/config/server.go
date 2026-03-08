package config

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hobnob/hobnob/server/secrets"
	"gopkg.in/yaml.v3"
)

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
func resolveServerSecrets(cfg *ServerConfig, provider secrets.Provider) error {
	fields := []*string{
		&cfg.Database.DSN,
		&cfg.Server.TLS.CertFile,
		&cfg.Server.TLS.KeyFile,
		&cfg.Server.TLS.CAFile,
	}

	for _, field := range fields {
		if strings.HasPrefix(*field, secretPrefix) {
			key := strings.TrimPrefix(*field, secretPrefix)
			val, err := provider.Get(context.Background(), key)
			if err != nil {
				return fmt.Errorf("resolving secret %q: %w", key, err)
			}
			*field = val
		}
	}

	return nil
}
