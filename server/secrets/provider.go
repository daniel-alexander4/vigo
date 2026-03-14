// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package secrets provides a pluggable interface for secret retrieval.
// The default backend is LocalProvider (AES-256-GCM encrypted files, always encrypted at rest).
// IsopassProvider wraps isopass for production.
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
