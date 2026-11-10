// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

// Package curators3 implements the read-only S3-compatible API over curator
// artifacts (ADR-027). This file is the SigV4 credential store; the SigV4
// verifier and the S3 listener + handlers land in later slices.
package curators3

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/secrets"
)

// credDerivePurpose is the HKDF info string binding an S3 secret_access_key to
// its access_key_id. Distinct from every other DeriveKey purpose (puddle,
// bolt, ops-puddle) so a leak of one derivation cannot cross a role boundary.
const credDerivePurpose = "vigo-curator-s3-cred-v1"

// accessKeyIDPrefix marks a Vigo-minted S3 access key id. The 4-char prefix +
// 16 base32 chars makes a 20-char id — the shape AWS access key ids take, so
// S3 tooling that loosely validates the field stays happy.
const accessKeyIDPrefix = "VIGO"

// CredStore mints and resolves SigV4 credentials for the curator S3 API.
// Derive-don't-store: the secret_access_key is recomputed from the master key
// via LocalProvider.DeriveKey at mint and again at every verify — never
// persisted. The DB row (curator_s3_credentials) is pure metadata.
type CredStore struct {
	queries *db.Queries
	local   *secrets.LocalProvider
	// names resolves a publisher friendly name to its founder pubkey hex at
	// mint time. Nil = only `*` and pubkey-hex scopes can be minted.
	names func(ctx context.Context) map[string]string
}

// SetNameResolver wires the friendly-name → founder-pubkey lookup Create uses
// to pin a name scope to a pubkey. Names come from envoy-reported claims that
// can change or be contested, so they are resolved once, when an operator
// mints the credential, and never again per request.
func (cs *CredStore) SetNameResolver(f func(ctx context.Context) map[string]string) {
	cs.names = f
}

// NewCredStore wires a CredStore. `local` is required — the SigV4 secret
// derivation roots in the master key, so the S3 API is local-backend-only
// (ADR-027). Callers under any other backend must not construct this store
// (the REST handler then 503s, and the S3 listener does not start).
func NewCredStore(queries *db.Queries, local *secrets.LocalProvider) *CredStore {
	return &CredStore{queries: queries, local: local}
}

// Created carries a freshly minted credential. The secret is returned once and
// never again — it is not stored; only the access_key_id round-trips.
type Created struct {
	AccessKeyID     string
	SecretAccessKey string
	Scope           string
	Name            string
}

// Resolved is a credential resolved for SigV4 verification — the stored
// metadata plus the freshly-derived secret_access_key.
type Resolved struct {
	Row    db.CuratorS3Credential
	Secret string
}

// Create mints a credential scoped to one publisher (`*` = every publisher).
// A publisher given by friendly name is resolved to its founder pubkey now
// and the pubkey is what is stored, so later name claims cannot re-point the
// credential at another publisher. Returns the access_key_id and the
// one-time secret_access_key.
func (cs *CredStore) Create(ctx context.Context, name, scope string) (Created, error) {
	scope, err := cs.resolveScope(ctx, scope)
	if err != nil {
		return Created{}, err
	}
	id, err := newAccessKeyID()
	if err != nil {
		return Created{}, err
	}
	secret, err := cs.deriveSecret(id)
	if err != nil {
		return Created{}, err
	}
	if err := cs.queries.CreateS3Credential(ctx, db.CreateS3CredentialParams{
		AccessKeyID: id,
		Scope:       scope,
		Name:        strings.TrimSpace(name),
	}); err != nil {
		return Created{}, fmt.Errorf("storing s3 credential: %w", err)
	}
	return Created{AccessKeyID: id, SecretAccessKey: secret, Scope: scope, Name: strings.TrimSpace(name)}, nil
}

// ErrUnknownPublisher is returned by Create when a name scope does not resolve
// to exactly one founder pubkey.
var ErrUnknownPublisher = errors.New("scope is not a known, uncontested publisher name")

// resolveScope normalizes a requested scope to `*` or a 64-hex founder pubkey.
func (cs *CredStore) resolveScope(ctx context.Context, scope string) (string, error) {
	scope = strings.TrimSpace(scope)
	switch {
	case scope == "" || scope == "*":
		return "*", nil
	case isHex64(scope):
		return strings.ToLower(scope), nil
	}
	if cs.names != nil {
		if pk := cs.names(ctx)[scope]; isHex64(pk) {
			return strings.ToLower(pk), nil
		}
	}
	return "", fmt.Errorf("%w: %q (pass the founder puddle pubkey as 64 hex characters)", ErrUnknownPublisher, scope)
}

// List returns all credential metadata. There are no secrets to redact — none
// are stored.
func (cs *CredStore) List(ctx context.Context) ([]db.CuratorS3Credential, error) {
	return cs.queries.ListS3Credentials(ctx)
}

// Revoke deletes a credential. A revoked access_key_id no longer resolves, so
// SigV4 verification fails closed.
func (cs *CredStore) Revoke(ctx context.Context, accessKeyID string) error {
	return cs.queries.DeleteS3Credential(ctx, accessKeyID)
}

// Lookup resolves an access_key_id for SigV4 verification — the stored row
// plus the freshly-derived secret. Returns nil (no error) when the id is
// unknown: revoked, or never minted.
func (cs *CredStore) Lookup(ctx context.Context, accessKeyID string) (*Resolved, error) {
	row, err := cs.queries.GetS3Credential(ctx, accessKeyID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading s3 credential: %w", err)
	}
	secret, err := cs.deriveSecret(accessKeyID)
	if err != nil {
		return nil, err
	}
	return &Resolved{Row: row, Secret: secret}, nil
}

// deriveSecret recomputes the secret_access_key for an access_key_id. The
// access_key_id is the HKDF salt, so each credential gets a distinct secret;
// the master key never leaves LocalProvider. The 32 derived bytes are
// base64-encoded — the form the operator configures into an S3 client and the
// form the SigV4 verifier feeds the signing-key chain.
func (cs *CredStore) deriveSecret(accessKeyID string) (string, error) {
	raw, err := cs.local.DeriveKey(credDerivePurpose, []byte(accessKeyID), 32)
	if err != nil {
		return "", fmt.Errorf("deriving s3 secret: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// newAccessKeyID generates a random VIGO-prefixed 20-char access key id.
func newAccessKeyID() (string, error) {
	b := make([]byte, 10) // 10 bytes → 16 base32 chars
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating access key id: %w", err)
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return accessKeyIDPrefix + enc, nil
}
