// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

// Package puddle implements the service-account puddle identity for the
// vigosrv runtime user (ADR-025). The on-disk envelope is parallel to the
// per-user puddle (`~/.vigo-puddle/identity.wrapped`) the Rust agent owns
// — same XChaCha20-Poly1305 layout, distinguished by a version byte —
// but the wrap key is derived from the secrets master key via HKDF
// instead of an interactive passphrase via Argon2id, so vigosrv can
// unlock its puddle identity autonomously at boot.
package puddle

import "path/filepath"

// DefaultDir is the canonical filesystem location for the service-account
// puddle's on-disk state. Operators rarely override this — the path
// mirrors the convention used by /srv/vigo/stacks, /srv/vigo/secrets, etc.
const DefaultDir = "/srv/vigo/puddle"

// File names inside the puddle dir. The per-user puddle uses identity.salt;
// the service-account puddle uses .salt (per ADR-025) to keep the salt
// inconspicuous in a directory listing and signal that it is not operator-
// editable.
const (
	WrappedFile = "identity.wrapped"
	SaltFile    = ".salt"
	PubkeyFile  = "identity.pub"
)

// WrappedPath returns the absolute path to the wrapped identity file.
func WrappedPath(dir string) string { return filepath.Join(dir, WrappedFile) }

// SaltPath returns the absolute path to the per-host HKDF salt file.
func SaltPath(dir string) string { return filepath.Join(dir, SaltFile) }

// PubkeyPath returns the absolute path to the (public, 0644) pubkey file.
func PubkeyPath(dir string) string { return filepath.Join(dir, PubkeyFile) }
