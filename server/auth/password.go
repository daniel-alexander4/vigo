// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"sync"

	"golang.org/x/crypto/bcrypt"
)

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

// dummyPasswordHash is a bcrypt hash at HashPassword's cost that no real
// account holds; see burnPasswordVerify.
var dummyPasswordHash = sync.OnceValue(func() string {
	h, _ := HashPassword("vigo-dummy-password-never-valid")
	return h
})

// passwordVerifier is the check burnPasswordVerify runs; a var so tests can
// count the calls.
var passwordVerifier = VerifyPassword

// burnPasswordVerify runs one bcrypt comparison against a dummy hash and
// discards the result. A login for an unknown user (or one with no password
// on file) calls it so that failure costs what a wrong password costs, and
// response time does not enumerate usernames.
func burnPasswordVerify(plaintext string) {
	_ = passwordVerifier(dummyPasswordHash(), plaintext)
}

// VerifyPassword reports whether plaintext matches a bcrypt hash produced by
// HashPassword. Returns false for any non-match, including a stored value that
// isn't a bcrypt hash at all (e.g. a pre-hash-at-rest plaintext password — that
// account must be reset via `vigocli webusers set-password`).
func VerifyPassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}
