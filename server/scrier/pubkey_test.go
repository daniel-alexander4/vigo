// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package scrier

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// fakeFetcher implements [sshPubkeyFetcher] with a canned response per
// user_id and an injectable error. Lets the helper be exercised without
// a real Queries / DB.
type fakeFetcher struct {
	pubkeys map[string]string
	err     error
}

func (f *fakeFetcher) GetUserSSHPublicKey(_ context.Context, id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	pk, ok := f.pubkeys[id]
	if !ok {
		return "", sql.ErrNoRows
	}
	return pk, nil
}

// A valid ed25519 pubkey for the parse-validation paths.
const testPubkey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEQiISM8yaMfJhysMR7ZM80c0w4lnwe/lgyPyzeSCEKI test@vigo"

// TestResolveVerifyPubkey_DBReadOverridesEmptySession is the load-bearing
// regression: an operator who runs `vigocli webusers set-role --role
// admin` mid-session and immediately attempts browser scrier gets the
// freshly-seeded DB pubkey, not the stale empty value from their
// session-cached User snapshot. Pre-fix, the websocket handler read
// `user.SSHPublicKey` directly from the session blob and surfaced
// "no SSH pubkey available" even though the DB column was populated;
// the workaround was log-out + log-back-in.
func TestResolveVerifyPubkey_DBReadOverridesEmptySession(t *testing.T) {
	f := &fakeFetcher{pubkeys: map[string]string{"u-1": testPubkey}}
	got, err := resolveVerifyPubkey(context.Background(), f, "u-1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != testPubkey {
		t.Errorf("verify pubkey = %q, want fresh DB value", got)
	}
}

// TestResolveVerifyPubkey_RequestedWins pins the CLI path: a connect
// request that carries an explicit pubkey wins over whatever the DB says
// for that user. Lets a CLI invocation under sudo use the operator's own
// key, not the target webuser's stored key.
func TestResolveVerifyPubkey_RequestedWins(t *testing.T) {
	f := &fakeFetcher{pubkeys: map[string]string{"u-1": "ssh-rsa AAAA-DB-key user@host"}}
	got, err := resolveVerifyPubkey(context.Background(), f, "u-1", testPubkey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != testPubkey {
		t.Errorf("requested pubkey should win, got %q", got)
	}
}

// TestResolveVerifyPubkey_RequestedInvalid: a malformed requested pubkey
// is a 400-class user error, not a fall-through to the DB.
func TestResolveVerifyPubkey_RequestedInvalid(t *testing.T) {
	f := &fakeFetcher{pubkeys: map[string]string{"u-1": testPubkey}}
	_, err := resolveVerifyPubkey(context.Background(), f, "u-1", "not a valid ssh key")
	if err == nil {
		t.Fatal("expected error on malformed requested pubkey")
	}
	if !strings.Contains(err.Error(), "invalid SSH pubkey") {
		t.Errorf("error message should name the failure mode, got: %v", err)
	}
}

// TestResolveVerifyPubkey_NoRowsIsEmpty: a missing user row decays to
// the empty string, not an error. The caller surfaces the operator-
// facing "no SSH pubkey available" message at the call site.
func TestResolveVerifyPubkey_NoRowsIsEmpty(t *testing.T) {
	f := &fakeFetcher{pubkeys: map[string]string{}}
	got, err := resolveVerifyPubkey(context.Background(), f, "u-missing", "")
	if err != nil {
		t.Fatalf("unexpected error on missing row: %v", err)
	}
	if got != "" {
		t.Errorf("missing user should resolve to empty pubkey, got %q", got)
	}
}

// TestResolveVerifyPubkey_DBErrorPropagates: a non-ErrNoRows DB failure
// surfaces as an error rather than being swallowed.
func TestResolveVerifyPubkey_DBErrorPropagates(t *testing.T) {
	boom := errors.New("connection refused")
	f := &fakeFetcher{err: boom}
	_, err := resolveVerifyPubkey(context.Background(), f, "u-1", "")
	if err == nil {
		t.Fatal("expected error on DB failure")
	}
	if !errors.Is(err, boom) {
		t.Errorf("DB error should propagate, got: %v", err)
	}
}

// TestResolveVerifyPubkey_NilFetcher: a nil queries handle decays to
// the empty string. Defensive against early-boot / misconfigured
// callers; the caller's empty-pubkey error surfaces a real
// operator-facing message.
func TestResolveVerifyPubkey_NilFetcher(t *testing.T) {
	got, err := resolveVerifyPubkey(context.Background(), nil, "u-1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("nil fetcher should resolve to empty, got %q", got)
	}
}

// A valid 1024-bit RSA pubkey (generated via `ssh-keygen -t rsa -b 1024`
// for this test fixture). Used to drive the non-Ed25519 rejection
// path: parses cleanly via ssh.ParseAuthorizedKey, then fails the
// ED25519 type assertion. 1024-bit is fine for a unit fixture — we're
// not using the key, only validating it gets rejected.
const testRSAPubkey = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQDTkv6Gqrk3xK/mEUUT4BIhFX0ZVmIuKWshqwV8fCS5USmIUwL/kGkdjQ1vW6BeVRJBUEo8f7fx6cUizbhRo1zHOlsPRJb+kIYlKS2R9IiFMvCgZDuUWjZZLqmf0URmZ9vC7pOZcnhL0QMd0o9F9GZM8wWAMVTy3u3rDtUsiFNA+Q== test@vigo"

// TestResolveVerifyPubkey_RequestedNonEd25519Rejected pins the
// downgrade-guard at the inline (CLI) path. A non-Ed25519 connect
// request (RSA here) is rejected with a clear error before the agent
// ever sees it. The scrier per-session contract is Ed25519-only.
func TestResolveVerifyPubkey_RequestedNonEd25519Rejected(t *testing.T) {
	f := &fakeFetcher{pubkeys: map[string]string{"u-1": testPubkey}}
	_, err := resolveVerifyPubkey(context.Background(), f, "u-1", testRSAPubkey)
	if err == nil {
		t.Fatal("expected error for non-Ed25519 requested pubkey")
	}
	if !strings.Contains(err.Error(), "ed25519") {
		t.Errorf("error should name the required algorithm, got: %v", err)
	}
}

// TestResolveVerifyPubkey_DBNonEd25519Rejected pins the symmetric
// downgrade-guard at the DB-read path — closes the vector where an
// admin storing an RSA key via `vigocli webusers update --ssh-key-file`
// would slip past inline-only validation. Defense-in-depth at the
// boundary that consumes the value.
func TestResolveVerifyPubkey_DBNonEd25519Rejected(t *testing.T) {
	f := &fakeFetcher{pubkeys: map[string]string{"u-1": testRSAPubkey}}
	_, err := resolveVerifyPubkey(context.Background(), f, "u-1", "")
	if err == nil {
		t.Fatal("expected error for non-Ed25519 DB pubkey")
	}
	if !strings.Contains(err.Error(), "ed25519") {
		t.Errorf("error should name the required algorithm, got: %v", err)
	}
}

// TestResolveVerifyPubkey_DBEmptyString: an empty (but no-err) DB row
// decays to "" — same as the sql.ErrNoRows path. Don't try to parse
// an empty string as an SSH key.
func TestResolveVerifyPubkey_DBEmptyString(t *testing.T) {
	f := &fakeFetcher{pubkeys: map[string]string{"u-1": ""}}
	got, err := resolveVerifyPubkey(context.Background(), f, "u-1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("empty DB pubkey should resolve to empty, got %q", got)
	}
}
