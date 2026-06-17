// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword returns a bcrypt hash of a web-user password, suitable for
// storage in the secrets vault. Web passwords are low-entropy and human-chosen,
// so they need a slow KDF (bcrypt) rather than the plain SHA-256 used for
// 256-bit-random API tokens. Mirrors the recovery-code hashing in totp.go.
// The CLI write path (cli.writeWebuserPassword) produces the stored value with
// bcrypt.DefaultCost; VerifyPassword below is its verifier.
func HashPassword(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword reports whether plaintext matches a bcrypt hash produced by
// HashPassword. Returns false for any non-match, including a stored value that
// isn't a bcrypt hash at all (e.g. a pre-hash-at-rest plaintext password — that
// account must be reset via `vigocli webusers set-password`).
func VerifyPassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}
