// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package bolt implements the per-vigosrv bolt identity for the
// spanner federation. Each vigosrv running in spanner mode holds an
// Ed25519 keypair distinct from the service-account puddle: the
// puddle signs for ops-puddle authority (stacks pushes), the bolt
// identity signs this bolt's admission row and every gossiped roster
// snapshot so peers can verify "this really comes from the bolt it
// claims to."
//
// On-disk shape mirrors server/puddle exactly — same XChaCha20-
// Poly1305 envelope (version byte 0x02), same per-host HKDF salt
// pattern, master-key-derived wrap key. The HKDF purpose is
// distinct (`vigo-bolt-wrap-v1`) so a theoretical master-key
// leak that only exposes one derivation cannot cross the bolt
// boundary into puddle-signing authority.
//
// Phase 1 holds the unwrapped signing key in vigosrv process
// memory for the reporter; Phase 2's CRDT layer extends this same
// identity for signed admissions + license attestations.
package bolt

import "path/filepath"

// DefaultDir is the canonical filesystem location for this
// vigosrv's bolt identity. Operators rarely override it — the
// path mirrors the convention used by /srv/vigo/puddle and
// /srv/vigo/ops-puddle.
const DefaultDir = "/srv/vigo/bolt"

// File names inside the bolt dir. Same convention as the
// service-account puddle (.salt is the per-host HKDF salt;
// identity.wrapped is the XChaCha20-Poly1305 envelope; identity.pub
// is the public-by-construction operator-readable pubkey file).
const (
	WrappedFile = "identity.wrapped"
	SaltFile    = ".salt"
	PubkeyFile  = "identity.pub"
)

func WrappedPath(dir string) string { return filepath.Join(dir, WrappedFile) }
func SaltPath(dir string) string    { return filepath.Join(dir, SaltFile) }
func PubkeyPath(dir string) string  { return filepath.Join(dir, PubkeyFile) }
