// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package scrier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// sshPubkeyFetcher is the narrow interface the scrier websocket uses to
// fetch a webuser's current SSH public key on the protocol-setup hot path.
// Implemented by `*db.Queries.GetUserSSHPublicKey` in production; a fake
// satisfies it in [TestResolveVerifyPubkey].
type sshPubkeyFetcher interface {
	GetUserSSHPublicKey(ctx context.Context, id string) (string, error)
}

// resolveVerifyPubkey returns the SSH public key the agent will scan
// authorized_keys files against. The CLI path wins (requestedPubkey is
// supplied inline at every connect — see `cli/scrier_ssh.go::resolveOperatorPubkey`);
// for the browser path, we read the current value of `users.ssh_public_key`
// directly from the DB rather than the session-cached `User.SSHPublicKey`.
//
// The session blob is a gob snapshot of the `auth.User` struct at login
// time. `ssh_public_key` has its own write-through lifecycle —
// `vigocli webusers create --role admin` and `vigocli webusers set-role
// --role admin` both auto-seed it from the matching OS user's home dir —
// and the session is not invalidated on those writes. Reading from the
// session was a stale-read trap: an operator who ran set-role mid-session
// and immediately tried browser scrier got "no SSH pubkey available"
// even though the DB column was populated. Logging out and back in was
// the workaround. Fixed by sourcing the pubkey from the DB at connect time.
//
// Returns (verifyPubkey, error). Errors:
//   - requestedPubkey is non-empty but fails to parse → "invalid SSH pubkey in connect request: …"
//   - DB lookup failed (other than sql.ErrNoRows; that decays to empty) → "fetching SSH pubkey: …"
//
// An empty return value (no requested key, no DB row, or DB row holds an
// empty string) is NOT an error — the caller surfaces the operator-
// facing "no SSH pubkey available" message at the call site.
func resolveVerifyPubkey(ctx context.Context, q sshPubkeyFetcher, userID, requestedPubkey string) (string, error) {
	if requestedPubkey != "" {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(requestedPubkey))
		if err != nil {
			return "", fmt.Errorf("invalid SSH pubkey in connect request: %w", err)
		}
		if key.Type() != ssh.KeyAlgoED25519 {
			return "", fmt.Errorf("scrier requires ed25519 pubkey, got %s", key.Type())
		}
		return requestedPubkey, nil
	}
	if q == nil {
		return "", nil
	}
	pubkey, err := q.GetUserSSHPublicKey(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("fetching SSH pubkey: %w", err)
	}
	if pubkey == "" {
		return "", nil
	}
	// Validate the DB-fetched key the same way as the inline-supplied one —
	// closes the forced-downgrade vector where an admin storing a non-Ed25519
	// key via `vigocli webusers update --ssh-key-file` would slip past
	// inline-only validation. Defense-in-depth at the read boundary.
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubkey))
	if err != nil {
		return "", fmt.Errorf("invalid SSH pubkey in DB for user %s: %w", userID, err)
	}
	if key.Type() != ssh.KeyAlgoED25519 {
		return "", fmt.Errorf("scrier requires ed25519 pubkey, got %s", key.Type())
	}
	return pubkey, nil
}
