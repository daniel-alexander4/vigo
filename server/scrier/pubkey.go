// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package scrier

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// sshPubkeyStore is the narrow interface the scrier websocket uses to read
// webusers' stored SSH public keys at session setup. Implemented by
// `*db.Queries` in production; a fake satisfies it in the pubkey tests.
type sshPubkeyStore interface {
	GetUserSSHPublicKey(ctx context.Context, id string) (string, error)
	ListUsers(ctx context.Context) ([]db.User, error)
}

// verifyIdentity is the outcome of [resolveVerifyPubkey]: the SSH public
// key the agent scans authorized_keys files for, and the webuser it is
// stored for. The session's DB row and audit entries are attributed to
// that webuser.
type verifyIdentity struct {
	Pubkey   string
	UserID   string
	Username string
}

// resolveVerifyPubkey returns the SSH public key the agent will scan
// authorized_keys files against, together with the webuser it belongs to.
//
// The verify key is always a key stored on a webuser row
// (`users.ssh_public_key`, set by `vigocli webusers create|update
// --ssh-key-file`). The agent lands the session as whichever OS user holds
// that key, so the stored key is what binds a webuser to one OS user
// (admin webusers map 1:1 to human OS users). A key in the connect request
// is never trusted on its own — it carries no proof of possession — so an
// admin cannot name some other user's public key and land as them:
//
//   - A real webuser (browser or CLI with a per-user token): the stored key
//     is the verify key. A requested key (the CLI sends one) must equal it.
//   - The local-admin principal (`sudo vigocli` on the server host, which
//     has no users row): the requested key is required and must be the
//     stored key of exactly one admin webuser; the session is attributed to
//     that webuser. The caller is root on the server host, so this adds
//     attribution rather than authority.
//
// The stored key is read from the DB at connect time, not from the
// session-cached `User.SSHPublicKey`: the session blob is a login-time
// snapshot and the key has its own write lifecycle.
//
// An empty Pubkey with a nil error means no key is stored for the caller;
// the call site surfaces the operator-facing "no SSH pubkey available"
// message.
func resolveVerifyPubkey(ctx context.Context, q sshPubkeyStore, user *vigoauth.User, requestedPubkey string) (verifyIdentity, error) {
	var requested ssh.PublicKey
	if requestedPubkey != "" {
		key, err := parseEd25519(requestedPubkey, "in connect request")
		if err != nil {
			return verifyIdentity{}, err
		}
		requested = key
	}
	if q == nil {
		return verifyIdentity{}, nil
	}

	if user.ID == vigoauth.LocalAdminUserID {
		return resolveLocalAdmin(ctx, q, requested, requestedPubkey)
	}

	stored, err := q.GetUserSSHPublicKey(ctx, user.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return verifyIdentity{}, fmt.Errorf("fetching SSH pubkey: %w", err)
	}
	if stored == "" {
		return verifyIdentity{}, nil
	}
	// Validate the stored key the same way as the inline one — closes the
	// forced-downgrade vector of a non-Ed25519 key stored via `vigocli
	// webusers update --ssh-key-file`.
	storedKey, err := parseEd25519(stored, "stored for user "+user.Username)
	if err != nil {
		return verifyIdentity{}, err
	}
	if requested != nil && !sameKey(requested, storedKey) {
		return verifyIdentity{}, fmt.Errorf("the SSH pubkey in the connect request is not the one stored for webuser %s — run `vigocli webusers update --username %s --ssh-key-file <your .pub>` or pass --ssh-key-file with the stored key", user.Username, user.Username)
	}
	return verifyIdentity{Pubkey: stored, UserID: user.ID, Username: user.Username}, nil
}

// resolveLocalAdmin maps the local-admin principal's requested key to the
// one admin webuser that stores it.
func resolveLocalAdmin(ctx context.Context, q sshPubkeyStore, requested ssh.PublicKey, requestedPubkey string) (verifyIdentity, error) {
	if requested == nil {
		return verifyIdentity{}, errors.New("the local-admin token has no stored SSH pubkey — the CLI must send your key (pass --ssh-key-file)")
	}
	users, err := q.ListUsers(ctx)
	if err != nil {
		return verifyIdentity{}, fmt.Errorf("listing webusers: %w", err)
	}
	var match *db.User
	for i := range users {
		u := &users[i]
		if u.Role != "admin" || u.SshPublicKey == "" {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(u.SshPublicKey))
		if err != nil || !sameKey(key, requested) {
			continue
		}
		if match != nil {
			return verifyIdentity{}, fmt.Errorf("SSH pubkey is stored for more than one admin webuser (%s, %s); scrier cannot attribute the session", match.Username, u.Username)
		}
		match = u
	}
	if match == nil {
		return verifyIdentity{}, errors.New("SSH pubkey is not stored for any admin webuser — run `vigocli webusers create|update --username <yourname> --role admin --ssh-key-file <your .pub>` first")
	}
	return verifyIdentity{Pubkey: requestedPubkey, UserID: match.ID, Username: match.Username}, nil
}

func parseEd25519(authorizedKey, where string) (ssh.PublicKey, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return nil, fmt.Errorf("invalid SSH pubkey %s: %w", where, err)
	}
	if key.Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("scrier requires ed25519 pubkey, got %s", key.Type())
	}
	return key, nil
}

// sameKey compares the wire encoding, so comments and whitespace in the
// authorized_keys line do not matter.
func sameKey(a, b ssh.PublicKey) bool {
	return bytes.Equal(a.Marshal(), b.Marshal())
}
