// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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

// thrallCtxKey is the context key for passing a cached thrall from the
// interceptor to the handler within a single request.
type thrallCtxKey struct{}

// ContextWithThrall stores a Thrall in the context for reuse by downstream
// handlers, avoiding duplicate DB lookups within the same gRPC request.
func ContextWithThrall(ctx context.Context, n Thrall) context.Context {
	return context.WithValue(ctx, thrallCtxKey{}, &n)
}

// ThrallFromContext retrieves a Thrall previously stored by the interceptor.
// Returns nil if no thrall is in the context.
func ThrallFromContext(ctx context.Context) *Thrall {
	n, _ := ctx.Value(thrallCtxKey{}).(*Thrall)
	return n
}
