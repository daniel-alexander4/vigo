// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

// Package secrets provides a pluggable interface for secret retrieval.
// The production implementation (IsopassProvider) wraps isopass;
// StaticProvider reads from a .env file for local dev and testing.
package secrets

import "context"

// Provider is the interface all secret backends must satisfy.
// The rest of the codebase depends only on this interface, never on a
// concrete implementation.
type Provider interface {
	// Get retrieves a secret string by its path/key (e.g. "hobnob/db/dsn").
	Get(ctx context.Context, key string) (string, error)

	// GetBytes retrieves a binary secret (e.g. a TLS cert/key PEM block).
	GetBytes(ctx context.Context, key string) ([]byte, error)

	// Close cleans up any connections or state held by the provider.
	Close() error
}
