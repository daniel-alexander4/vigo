package secrets

import (
	"fmt"
	"os"
)

// Config describes which secrets backend to use and its settings.
// Values in this struct must not use "secret:" prefixes — this config
// is loaded before the provider exists.
type Config struct {
	Backend  string `yaml:"backend"`   // "static" or "isopass"
	EnvFile  string `yaml:"env_file"`  // static: path to .env file
	URL      string `yaml:"url"`       // isopass: API base URL
	TokenEnv string `yaml:"token_env"` // isopass: env var containing bearer token
}

// NewFromConfig creates a Provider based on the given config.
// If Backend is empty, it defaults to "static".
func NewFromConfig(cfg Config) (Provider, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = "static"
	}

	switch backend {
	case "static":
		envFile := cfg.EnvFile
		if envFile == "" {
			envFile = ".env"
		}
		return NewStaticProvider(envFile)
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
		return NewIsopassProvider(cfg.URL, token)
	default:
		return nil, fmt.Errorf("unknown secrets backend %q (expected \"static\" or \"isopass\")", backend)
	}
}
