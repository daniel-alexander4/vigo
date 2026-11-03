// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package puddle

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// HKDF purpose string. Versioned so master-key rotation policies can
// distinguish past derivations. The version is independent of the
// envelope WrapVersion below — the envelope versions the on-disk
// bytes; the purpose versions the key-derivation contract.
const HKDFPurpose = "vigo-puddle-wrap-v1"

// On-disk envelope. Matches the per-user puddle's wrap layout (see
// agent/src/swarm/lockbox/crypt.rs) so future readers can dispatch on
// the version byte to choose the correct derivation:
//
//	[1 byte version]
//	[24 byte XChaCha20-Poly1305 nonce]
//	[ciphertext: 32 byte Ed25519 seed + 16 byte Poly1305 tag]
//
// WrapVersion 0x01 is the per-user, Argon2id-derived envelope.
// WrapVersion 0x02 is the service-account, HKDF-derived envelope.
const (
	WrapVersion byte = 0x02
	nonceLen         = chacha20poly1305.NonceSizeX
	seedLen          = ed25519.SeedSize // 32
	tagLen           = chacha20poly1305.Overhead
	wrapKeyLen       = chacha20poly1305.KeySize // 32
	envelopeMin      = 1 + nonceLen + seedLen + tagLen
)

// SaltLen is the size of the per-host HKDF salt. 32 bytes gives ample
// entropy for distinguishing distinct hosts that share a master key
// (the unusual but supported "secrets-dir replicated across nodes"
// case the ADR calls out).
const SaltLen = 32

// freshSalt returns SaltLen cryptographically-random bytes.
func freshSalt() ([]byte, error) {
	s := make([]byte, SaltLen)
	if _, err := io.ReadFull(rand.Reader, s); err != nil {
		return nil, fmt.Errorf("salt: %w", err)
	}
	return s, nil
}

// wrap encrypts seed (the 32-byte Ed25519 seed) under wrapKey using
// XChaCha20-Poly1305 with a fresh random nonce and returns the on-disk
// envelope.
func wrap(seed, wrapKey []byte) ([]byte, error) {
	if len(seed) != seedLen {
		return nil, fmt.Errorf("seed must be %d bytes, got %d", seedLen, len(seed))
	}
	if len(wrapKey) != wrapKeyLen {
		return nil, fmt.Errorf("wrap key must be %d bytes, got %d", wrapKeyLen, len(wrapKey))
	}
	aead, err := chacha20poly1305.NewX(wrapKey)
	if err != nil {
		return nil, fmt.Errorf("xchacha20poly1305: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	out := make([]byte, 0, 1+nonceLen+seedLen+tagLen)
	out = append(out, WrapVersion)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, seed, nil)
	return out, nil
}

// unwrap reverses wrap. Returns the 32-byte seed.
func unwrap(envelope, wrapKey []byte) ([]byte, error) {
	if len(envelope) < envelopeMin {
		return nil, fmt.Errorf("envelope is %d bytes, expected at least %d", len(envelope), envelopeMin)
	}
	if envelope[0] != WrapVersion {
		return nil, fmt.Errorf("envelope has unknown version byte 0x%02x (expected 0x%02x)", envelope[0], WrapVersion)
	}
	if len(wrapKey) != wrapKeyLen {
		return nil, fmt.Errorf("wrap key must be %d bytes, got %d", wrapKeyLen, len(wrapKey))
	}
	aead, err := chacha20poly1305.NewX(wrapKey)
	if err != nil {
		return nil, fmt.Errorf("xchacha20poly1305: %w", err)
	}
	nonce := envelope[1 : 1+nonceLen]
	ciphertext := envelope[1+nonceLen:]
	seed, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("unwrap failed: wrong master key, corrupted envelope, or mismatched per-host salt")
	}
	if len(seed) != seedLen {
		return nil, fmt.Errorf("unwrapped seed is %d bytes, expected %d", len(seed), seedLen)
	}
	return seed, nil
}
