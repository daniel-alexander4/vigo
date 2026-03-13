// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// builtinDefaults are sane zero-config defaults for core secrets so the
// server starts out of the box without a .env file or env vars.
var builtinDefaults = map[string]string{
	"HOBNOB_DB_DSN":      "hobnob.db",
	"HOBNOB_TLS_CERT":    "certs/server.crt",
	"HOBNOB_TLS_KEY":     "certs/server.key",
	"HOBNOB_TLS_CA":      "certs/ca.crt",
	"HOBNOB_TLS_CA_KEY":  "certs/ca.key",
}

// StaticProvider resolves secrets from three layers (first match wins):
//  1. .env file (if present)
//  2. OS environment variables
//  3. Built-in defaults for core keys
//
// This is the default backend. In production with external secret
// management, use IsopassProvider instead.
type StaticProvider struct {
	secrets map[string]string
}

// NewStaticProvider loads secrets from the given .env file path.
// If the file does not exist, the provider still works — it falls back to
// environment variables and built-in defaults.
// Key paths like "hobnob/db/dsn" are resolved by converting to the
// environment variable name HOBNOB_DB_DSN (uppercase, slashes to underscores).
func NewStaticProvider(path string) (*StaticProvider, error) {
	env, err := godotenv.Read(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading .env file %q: %w", path, err)
	}
	if env == nil {
		env = make(map[string]string)
	}
	return &StaticProvider{secrets: env}, nil
}

// Get resolves a secret key path to its value.
// Key format: "hobnob/db/dsn" -> env var HOBNOB_DB_DSN.
// Lookup order: .env file, OS environment, built-in defaults.
// Returns an error if the key is not found in any layer.
func (p *StaticProvider) Get(_ context.Context, key string) (string, error) {
	envKey := pathToEnvVar(key)

	// 1. .env file
	if val, ok := p.secrets[envKey]; ok {
		return val, nil
	}

	// 2. OS environment variable
	if val := os.Getenv(envKey); val != "" {
		return val, nil
	}

	// 3. Built-in defaults
	if val, ok := builtinDefaults[envKey]; ok {
		return val, nil
	}

	return "", fmt.Errorf("secret %q (env var %s) not found", key, envKey)
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

// pathToEnvVar converts a secret path like "hobnob/db/dsn" to an
// environment variable name like "HOBNOB_DB_DSN".
func pathToEnvVar(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, "/", "_"))
}
