// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// ParsePubKey decodes a PEM-encoded ED25519 public key.
func ParsePubKey(pemStr string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("invalid PEM block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing PKIX public key: %w", err)
	}
	pubKey, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key is not ED25519")
	}
	return pubKey, nil
}

// envoyCtxKey is the context key for passing a cached envoy from the
// interceptor to the handler within a single request.
type envoyCtxKey struct{}

// ContextWithEnvoy stores a Envoy in the context for reuse by downstream
// handlers, avoiding duplicate DB lookups within the same gRPC request.
func ContextWithEnvoy(ctx context.Context, n Envoy) context.Context {
	return context.WithValue(ctx, envoyCtxKey{}, &n)
}

// EnvoyFromContext retrieves a Envoy previously stored by the interceptor.
// Returns nil if no envoy is in the context.
func EnvoyFromContext(ctx context.Context) *Envoy {
	n, _ := ctx.Value(envoyCtxKey{}).(*Envoy)
	return n
}
