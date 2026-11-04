// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package scrier

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// fakeStore implements [sshPubkeyStore] with canned rows and an injectable
// error, so the helper is exercised without a real DB.
type fakeStore struct {
	users []db.User
	err   error
}

func (f *fakeStore) GetUserSSHPublicKey(_ context.Context, id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	for _, u := range f.users {
		if u.ID == id {
			return u.SshPublicKey, nil
		}
	}
	return "", sql.ErrNoRows
}

func (f *fakeStore) ListUsers(_ context.Context) ([]db.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.users, nil
}

// Valid ed25519 pubkeys for the parse-validation paths.
const (
	testPubkey      = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEQiISM8yaMfJhysMR7ZM80c0w4lnwe/lgyPyzeSCEKI test@vigo"
	testOtherPubkey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBTnhQDY+EoHzuH6lTBahbzZVGcRcGEjhBBg0CLVK+cd other@vigo"
)

func alice() *vigoauth.User { return &vigoauth.User{ID: "u-1", Username: "alice", Role: "admin"} }

func localAdmin() *vigoauth.User {
	return &vigoauth.User{ID: vigoauth.LocalAdminUserID, Username: vigoauth.LocalAdminUserID, Role: "admin"}
}

func storeWith(rows ...db.User) *fakeStore { return &fakeStore{users: rows} }

func row(id, username, role, key string) db.User {
	return db.User{ID: id, Username: username, Role: role, SshPublicKey: key}
}

// TestResolveVerifyPubkey_StoredKeyIsVerifySource: the browser path (no
// requested key) uses the key read fresh from the DB, not the session's
// login-time snapshot.
func TestResolveVerifyPubkey_StoredKeyIsVerifySource(t *testing.T) {
	got, err := resolveVerifyPubkey(context.Background(), storeWith(row("u-1", "alice", "admin", testPubkey)), alice(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Pubkey != testPubkey || got.UserID != "u-1" || got.Username != "alice" {
		t.Errorf("got %+v, want alice's stored key", got)
	}
}

// TestResolveVerifyPubkey_RequestedMatchingStoredAccepted: the CLI path
// with the caller's own key passes; the comment may differ.
func TestResolveVerifyPubkey_RequestedMatchingStoredAccepted(t *testing.T) {
	requested := strings.TrimSuffix(testPubkey, "test@vigo") + "laptop"
	got, err := resolveVerifyPubkey(context.Background(), storeWith(row("u-1", "alice", "admin", testPubkey)), alice(), requested)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Pubkey != testPubkey {
		t.Errorf("verify key = %q, want the stored key", got.Pubkey)
	}
}

// TestResolveVerifyPubkey_RequestedNotStoredRejected is the regression: an
// admin naming another user's public key in the connect request must not
// get it used as the verify key (it would land them as that OS user).
func TestResolveVerifyPubkey_RequestedNotStoredRejected(t *testing.T) {
	s := storeWith(row("u-1", "alice", "admin", testPubkey), row("u-2", "bob", "admin", testOtherPubkey))
	_, err := resolveVerifyPubkey(context.Background(), s, alice(), testOtherPubkey)
	if err == nil {
		t.Fatal("a requested key that is not the caller's stored key must be refused")
	}
	if !strings.Contains(err.Error(), "not the one stored") {
		t.Errorf("error should name the mismatch, got: %v", err)
	}
}

// TestResolveVerifyPubkey_RequestedWithNoStoredKeyIsEmpty: a caller with
// no stored key cannot supply one inline.
func TestResolveVerifyPubkey_RequestedWithNoStoredKeyIsEmpty(t *testing.T) {
	got, err := resolveVerifyPubkey(context.Background(), storeWith(row("u-1", "alice", "admin", "")), alice(), testPubkey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Pubkey != "" {
		t.Errorf("no stored key must resolve empty, got %q", got.Pubkey)
	}
}

// TestResolveVerifyPubkey_LocalAdminMapsToOwningAdmin: the local-admin
// token's requested key resolves to the admin webuser storing it, and the
// session is attributed to that webuser.
func TestResolveVerifyPubkey_LocalAdminMapsToOwningAdmin(t *testing.T) {
	s := storeWith(row("u-1", "alice", "admin", testPubkey), row("u-2", "bob", "admin", testOtherPubkey))
	got, err := resolveVerifyPubkey(context.Background(), s, localAdmin(), testOtherPubkey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.UserID != "u-2" || got.Username != "bob" || got.Pubkey != testOtherPubkey {
		t.Errorf("got %+v, want bob", got)
	}
}

// TestResolveVerifyPubkey_LocalAdminUnknownKeyRejected: a key no admin
// webuser stores is refused, and so is one stored only on a viewer.
func TestResolveVerifyPubkey_LocalAdminUnknownKeyRejected(t *testing.T) {
	s := storeWith(row("u-1", "alice", "admin", testPubkey), row("u-3", "carol", "viewer", testOtherPubkey))
	if _, err := resolveVerifyPubkey(context.Background(), s, localAdmin(), testOtherPubkey); err == nil {
		t.Fatal("a key stored only on a non-admin webuser must be refused")
	}
	if _, err := resolveVerifyPubkey(context.Background(), storeWith(), localAdmin(), testPubkey); err == nil {
		t.Fatal("a key stored on no webuser must be refused")
	}
}

// TestResolveVerifyPubkey_LocalAdminAmbiguousRejected: two admin rows
// sharing a key cannot be attributed.
func TestResolveVerifyPubkey_LocalAdminAmbiguousRejected(t *testing.T) {
	s := storeWith(row("u-1", "alice", "admin", testPubkey), row("u-2", "bob", "admin", testPubkey))
	if _, err := resolveVerifyPubkey(context.Background(), s, localAdmin(), testPubkey); err == nil {
		t.Fatal("a key shared by two admin webusers must be refused")
	}
}

// TestResolveVerifyPubkey_LocalAdminNeedsRequestedKey: the synthetic
// principal has no stored key, so it must send one.
func TestResolveVerifyPubkey_LocalAdminNeedsRequestedKey(t *testing.T) {
	if _, err := resolveVerifyPubkey(context.Background(), storeWith(row("u-1", "alice", "admin", testPubkey)), localAdmin(), ""); err == nil {
		t.Fatal("local admin without a requested key must be refused")
	}
}

// TestResolveVerifyPubkey_RequestedInvalid: a malformed requested pubkey
// is an error, not a fall-through to the DB.
func TestResolveVerifyPubkey_RequestedInvalid(t *testing.T) {
	_, err := resolveVerifyPubkey(context.Background(), storeWith(row("u-1", "alice", "admin", testPubkey)), alice(), "not a valid ssh key")
	if err == nil {
		t.Fatal("expected error on malformed requested pubkey")
	}
	if !strings.Contains(err.Error(), "invalid SSH pubkey") {
		t.Errorf("error message should name the failure mode, got: %v", err)
	}
}

// TestResolveVerifyPubkey_NoRowsIsEmpty: a missing user row decays to an
// empty key; the caller surfaces "no SSH pubkey available".
func TestResolveVerifyPubkey_NoRowsIsEmpty(t *testing.T) {
	got, err := resolveVerifyPubkey(context.Background(), storeWith(), alice(), "")
	if err != nil {
		t.Fatalf("unexpected error on missing row: %v", err)
	}
	if got.Pubkey != "" {
		t.Errorf("missing user should resolve to empty pubkey, got %q", got.Pubkey)
	}
}

// TestResolveVerifyPubkey_DBErrorPropagates: a non-ErrNoRows DB failure
// surfaces as an error rather than being swallowed.
func TestResolveVerifyPubkey_DBErrorPropagates(t *testing.T) {
	boom := errors.New("connection refused")
	_, err := resolveVerifyPubkey(context.Background(), &fakeStore{err: boom}, alice(), "")
	if !errors.Is(err, boom) {
		t.Errorf("DB error should propagate, got: %v", err)
	}
}

// TestResolveVerifyPubkey_NilStore: no DB handle decays to empty.
func TestResolveVerifyPubkey_NilStore(t *testing.T) {
	got, err := resolveVerifyPubkey(context.Background(), nil, alice(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Pubkey != "" {
		t.Errorf("nil store should resolve to empty, got %q", got.Pubkey)
	}
}

// A valid 1024-bit RSA pubkey, used to drive the non-Ed25519 rejection.
const testRSAPubkey = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQDTkv6Gqrk3xK/mEUUT4BIhFX0ZVmIuKWshqwV8fCS5USmIUwL/kGkdjQ1vW6BeVRJBUEo8f7fx6cUizbhRo1zHOlsPRJb+kIYlKS2R9IiFMvCgZDuUWjZZLqmf0URmZ9vC7pOZcnhL0QMd0o9F9GZM8wWAMVTy3u3rDtUsiFNA+Q== test@vigo"

// TestResolveVerifyPubkey_RequestedNonEd25519Rejected pins the downgrade
// guard at the inline (CLI) path.
func TestResolveVerifyPubkey_RequestedNonEd25519Rejected(t *testing.T) {
	_, err := resolveVerifyPubkey(context.Background(), storeWith(row("u-1", "alice", "admin", testPubkey)), alice(), testRSAPubkey)
	if err == nil || !strings.Contains(err.Error(), "ed25519") {
		t.Errorf("non-Ed25519 requested key must be refused naming ed25519, got: %v", err)
	}
}

// TestResolveVerifyPubkey_DBNonEd25519Rejected pins the same guard at the
// DB-read path.
func TestResolveVerifyPubkey_DBNonEd25519Rejected(t *testing.T) {
	_, err := resolveVerifyPubkey(context.Background(), storeWith(row("u-1", "alice", "admin", testRSAPubkey)), alice(), "")
	if err == nil || !strings.Contains(err.Error(), "ed25519") {
		t.Errorf("non-Ed25519 stored key must be refused naming ed25519, got: %v", err)
	}
}
