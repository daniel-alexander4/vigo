// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package secrets provides a pluggable interface for secret retrieval.
// The default backend is LocalProvider (AES-256-GCM encrypted files, always encrypted at rest).
// IsopassProvider wraps isopass for production.
package secrets

import "context"

// NullProvider is a no-op secrets provider that returns empty strings for all
// keys. Used by offline tools (hobnobcli test, hobnobcli validate) that need
// to load config without resolving secrets.
type NullProvider struct{}

func (NullProvider) Get(_ context.Context, _ string) (string, error)      { return "", nil }
func (NullProvider) GetBytes(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (NullProvider) Close() error                                          { return nil }

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
