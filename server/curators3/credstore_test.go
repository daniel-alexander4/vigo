// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package curators3

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/secrets"
)

func newTestCredStore(t *testing.T) *CredStore {
	t.Helper()
	dir := t.TempDir()
	sqlDB, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "test.db")+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.Exec(`
CREATE TABLE curator_s3_credentials (
    access_key_id TEXT PRIMARY KEY,
    scope         TEXT NOT NULL DEFAULT '*',
    name          TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);`); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	lp, err := secrets.NewLocalProvider("", filepath.Join(dir, "secrets"))
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	return NewCredStore(db.New(sqlDB), lp)
}

func TestCreateLookupRoundTrip(t *testing.T) {
	cs := newTestCredStore(t)
	ctx := context.Background()

	created, err := cs.Create(ctx, "ci-runner", testPubkey)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.AccessKeyID == "" || created.SecretAccessKey == "" {
		t.Fatal("Create returned an empty id or secret")
	}
	if created.Scope != testPubkey {
		t.Errorf("scope = %q, want testPubkey", created.Scope)
	}

	got, err := cs.Lookup(ctx, created.AccessKeyID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("Lookup returned nil for a freshly minted credential")
	}
	// Derive-don't-store: Lookup must re-derive the exact secret Create
	// returned — the secret is never persisted, only recomputed.
	if got.Secret != created.SecretAccessKey {
		t.Error("Lookup re-derived a different secret than Create returned")
	}
	if got.Row.AccessKeyID != created.AccessKeyID || got.Row.Scope != testPubkey {
		t.Errorf("Lookup row mismatch: %+v", got.Row)
	}
}

func TestCreateDefaultsScopeToWildcard(t *testing.T) {
	cs := newTestCredStore(t)
	created, err := cs.Create(context.Background(), "x", "  ")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Scope != "*" {
		t.Errorf("blank scope = %q, want *", created.Scope)
	}
}

func TestRevokeMakesLookupFailClosed(t *testing.T) {
	cs := newTestCredStore(t)
	ctx := context.Background()
	created, err := cs.Create(ctx, "tmp", "*")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := cs.Revoke(ctx, created.AccessKeyID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got, err := cs.Lookup(ctx, created.AccessKeyID)
	if err != nil {
		t.Fatalf("Lookup after revoke: %v", err)
	}
	if got != nil {
		t.Error("Lookup resolved a revoked credential — must fail closed")
	}
}

func TestLookupUnknownReturnsNil(t *testing.T) {
	cs := newTestCredStore(t)
	got, err := cs.Lookup(context.Background(), "VIGONOSUCHKEYAAAAAAA")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Error("Lookup of an unknown id returned non-nil")
	}
}

func TestAccessKeyIDShape(t *testing.T) {
	id, err := newAccessKeyID()
	if err != nil {
		t.Fatalf("newAccessKeyID: %v", err)
	}
	if len(id) != 20 {
		t.Errorf("access key id len = %d, want 20 (%q)", len(id), id)
	}
	if id[:len(accessKeyIDPrefix)] != accessKeyIDPrefix {
		t.Errorf("access key id %q lacks the %q prefix", id, accessKeyIDPrefix)
	}
	id2, _ := newAccessKeyID()
	if id == id2 {
		t.Error("newAccessKeyID returned the same id twice")
	}
}
