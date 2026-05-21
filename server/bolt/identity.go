// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package bolt

import (
	"crypto/ed25519"

	"git.alexander4.io/vigo/server/puddle"
)

// HKDFPurpose binds the bolt wrap-key derivation to the bolt
// role. Distinct from puddle.HKDFPurpose ("vigo-puddle-wrap-v1") and
// puddle.OpsHKDFPurpose ("vigo-ops-puddle-wrap-v1") so a theoretical
// master-key leak that only exposes one derivation cannot cross
// the role boundary.
const HKDFPurpose = "vigo-bolt-wrap-v1"

// Identity is an unlocked bolt signing identity. The signing
// key is held in memory only — wipe by dropping the reference.
// The shape mirrors puddle.Identity so the two are interchangeable
// at the carrying-data level.
type Identity = puddle.Identity

// EnsureIdentity loads the bolt identity from dir, generating it
// on first call. On-disk shape is identical to a service-account
// puddle (wrapped envelope at identity.wrapped, salt at .salt,
// pubkey at identity.pub) — the HKDF purpose is the only thing that
// distinguishes the two derivations.
//
// First-call semantics: directory created (0700), Ed25519 keypair
// generated, fresh 32-byte HKDF salt persisted, seed wrapped under
// the secrets master key, all three files written atomically.
//
// Subsequent calls re-derive the wrap key from the same salt and
// unwrap. Master-key rotation invalidates the wrap and surfaces an
// unwrap error — recovery is operator-driven (regenerate the
// identity; the resulting new pubkey forces re-pin on every hub
// the bolt reports to).
func EnsureIdentity(deriver puddle.KeyDeriver, dir string) (*Identity, error) {
	return puddle.EnsureIdentityWithPurpose(deriver, dir, HKDFPurpose)
}

// LoadIdentity unwraps an existing bolt identity. Errors if
// the wrapped file is missing — callers wanting first-time
// generation use EnsureIdentity.
func LoadIdentity(deriver puddle.KeyDeriver, dir string) (*Identity, error) {
	return puddle.LoadIdentityWithPurpose(deriver, dir, HKDFPurpose)
}

// Compile-time check: the carried key types are what the reporter
// + hub verify code expects. (Pure documentation; if this fails
// to compile, an upstream type change broke the contract.)
var (
	_ ed25519.PrivateKey = (Identity{}).SigningKey
	_ ed25519.PublicKey  = (Identity{}).PublicKey
)
