// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"sync"
	"time"
)

// cachedThrall pairs a DB thrall with its parsed pubkey and fetch time.
type cachedThrall struct {
	thrall      Thrall
	pubKey    ed25519.PublicKey // parsed once, reused on every RPC
	fetchedAt time.Time
}

// ThrallCache provides a TTL-based in-memory cache for thrall lookups,
// eliminating repeated GetThrallByID queries during a single check-in cycle.
// Also caches the parsed ED25519 public key to avoid PEM decode + x509 parse
// on every signature verification. Thread-safe for concurrent access.
type ThrallCache struct {
	mu      sync.RWMutex
	entries map[string]*cachedThrall
	queries *Queries
	ttl     time.Duration
}

// NewThrallCache creates a thrall cache backed by the given Queries with the
// specified TTL. Entries are lazily evicted on access after TTL expires.
func NewThrallCache(queries *Queries, ttl time.Duration) *ThrallCache {
	return &ThrallCache{
		entries: make(map[string]*cachedThrall),
		queries: queries,
		ttl:     ttl,
	}
}

// GetByID returns the thrall for the given UUID string, serving from cache if the
// entry is fresh (within TTL). On cache miss or stale entry, fetches from DB
// and updates the cache. Returns sql.ErrNoRows if the thrall does not exist.
func (nc *ThrallCache) GetByID(ctx context.Context, id string) (Thrall, error) {
	nc.mu.RLock()
	if entry, ok := nc.entries[id]; ok && time.Since(entry.fetchedAt) < nc.ttl {
		n := entry.thrall
		nc.mu.RUnlock()
		return n, nil
	}
	nc.mu.RUnlock()

	// Cache miss or stale — fetch from DB.
	thrall, err := nc.queries.GetThrallByID(ctx, id)
	if err != nil {
		return Thrall{}, err
	}

	// Parse pubkey eagerly so it's cached for signature verification.
	pubKey, _ := parsePubKey(thrall.PubkeyPem)

	nc.mu.Lock()
	nc.entries[id] = &cachedThrall{thrall: thrall, pubKey: pubKey, fetchedAt: time.Now()}
	nc.mu.Unlock()

	return thrall, nil
}

// GetByIDWithPubKey returns the thrall and its parsed ED25519 public key,
// serving both from cache when available. Eliminates PEM decode + x509 parse
// on every signature verification RPC.
func (nc *ThrallCache) GetByIDWithPubKey(ctx context.Context, id string) (Thrall, ed25519.PublicKey, error) {
	nc.mu.RLock()
	if entry, ok := nc.entries[id]; ok && time.Since(entry.fetchedAt) < nc.ttl {
		nc.mu.RUnlock()
		return entry.thrall, entry.pubKey, nil
	}
	nc.mu.RUnlock()

	// Cache miss or stale — fetch from DB.
	thrall, err := nc.queries.GetThrallByID(ctx, id)
	if err != nil {
		return Thrall{}, nil, err
	}

	pubKey, pkErr := parsePubKey(thrall.PubkeyPem)
	if pkErr != nil {
		return Thrall{}, nil, pkErr
	}

	nc.mu.Lock()
	nc.entries[id] = &cachedThrall{thrall: thrall, pubKey: pubKey, fetchedAt: time.Now()}
	nc.mu.Unlock()

	return thrall, pubKey, nil
}

// Invalidate removes a thrall from the cache, forcing the next lookup to
// hit the database. Used after revocation to ensure immediate effect.
func (nc *ThrallCache) Invalidate(id string) {
	nc.mu.Lock()
	delete(nc.entries, id)
	nc.mu.Unlock()
}

// parsePubKey decodes a PEM-encoded ED25519 public key.
func parsePubKey(pemStr string) (ed25519.PublicKey, error) {
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

// IsNotFound returns true if the error is sql.ErrNoRows.
func IsNotFound(err error) bool {
	return err == sql.ErrNoRows
}
