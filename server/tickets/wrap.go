// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tickets

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// On-disk envelope:
//
//	[1 byte version]
//	[24 byte XChaCha20-Poly1305 nonce]
//	[ciphertext: N * 32 bytes of ticket keys + 16 byte Poly1305 tag]
//
// N is recovered from len(ciphertext) - tagLen, divided by KeyLen.
// Single version (0x01) for now; future rotation-format changes get
// their own version byte and a dispatch in unwrap.
const WrapVersion byte = 0x01

const (
	KeyLen  = 32 // each TLS session-ticket key is 32 bytes
	SaltLen = 32

	nonceLen    = chacha20poly1305.NonceSizeX
	tagLen      = chacha20poly1305.Overhead
	wrapKeyLen  = chacha20poly1305.KeySize // 32
	envelopeMin = 1 + nonceLen + KeyLen + tagLen
)

func freshSalt() ([]byte, error) {
	s := make([]byte, SaltLen)
	if _, err := io.ReadFull(rand.Reader, s); err != nil {
		return nil, fmt.Errorf("salt: %w", err)
	}
	return s, nil
}

// wrap encrypts payload (concatenated 32-byte ticket keys) under wrapKey
// with XChaCha20-Poly1305 and a fresh random nonce. Returns the on-disk
// envelope bytes.
func wrap(payload, wrapKey []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload)%KeyLen != 0 {
		return nil, fmt.Errorf("payload must be a multiple of %d bytes, got %d", KeyLen, len(payload))
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
	out := make([]byte, 0, 1+nonceLen+len(payload)+tagLen)
	out = append(out, WrapVersion)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, payload, nil)
	return out, nil
}

// unwrap reverses wrap, returning the concatenated 32-byte ticket keys.
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
	payload, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("unwrap failed: wrong master key, corrupted envelope, or mismatched per-host salt")
	}
	if len(payload) == 0 || len(payload)%KeyLen != 0 {
		return nil, fmt.Errorf("unwrapped payload is %d bytes, not a multiple of %d", len(payload), KeyLen)
	}
	return payload, nil
}
