package secrets

import (
	"context"
	"fmt"
	"strings"

	"github.com/joho/godotenv"
)

// StaticProvider reads secrets from a .env file (via godotenv).
// It is used for local development and testing only. In production,
// IsopassProvider should be used instead.
type StaticProvider struct {
	secrets map[string]string
}

// NewStaticProvider loads secrets from the given .env file path.
// Key paths like "isogov/db/dsn" are resolved by converting to the
// environment variable name ISOGOV_DB_DSN (uppercase, slashes to underscores).
func NewStaticProvider(path string) (*StaticProvider, error) {
	env, err := godotenv.Read(path)
	if err != nil {
		return nil, fmt.Errorf("reading .env file %q: %w", path, err)
	}
	return &StaticProvider{secrets: env}, nil
}

// Get resolves a secret key path to its value.
// Key format: "isogov/db/dsn" -> env var ISOGOV_DB_DSN.
// Returns an error if the key is not found (fail-fast, never return empty).
func (p *StaticProvider) Get(_ context.Context, key string) (string, error) {
	envKey := pathToEnvVar(key)
	val, ok := p.secrets[envKey]
	if !ok {
		return "", fmt.Errorf("secret %q (env var %s) not found in .env file", key, envKey)
	}
	return val, nil
}

// GetBytes returns the secret as a byte slice.
func (p *StaticProvider) GetBytes(ctx context.Context, key string) ([]byte, error) {
	val, err := p.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return []byte(val), nil
}

// Close is a no-op for StaticProvider.
func (p *StaticProvider) Close() error {
	return nil
}

// pathToEnvVar converts a secret path like "isogov/db/dsn" to an
// environment variable name like "ISOGOV_DB_DSN".
func pathToEnvVar(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, "/", "_"))
}
