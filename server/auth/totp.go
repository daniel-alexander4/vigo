// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

// TOTPIssuer is the issuer name shown in authenticator apps.
const TOTPIssuer = "Hobnob"

// recoveryCodeCount is the number of recovery codes generated.
const recoveryCodeCount = 8

// GenerateTOTPSecret creates a new TOTP secret for the given username.
// Returns the secret key (base32) and the otpauth:// URL for QR codes.
func GenerateTOTPSecret(username string) (secret, url string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      TOTPIssuer,
		AccountName: username,
	})
	if err != nil {
		return "", "", fmt.Errorf("generating TOTP key: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// ValidateTOTPCode checks a 6-digit TOTP code against the secret.
func ValidateTOTPCode(code, secret string) bool {
	return totp.Validate(code, secret)
}

// GenerateRecoveryCodes creates recoveryCodeCount random codes and returns
// them as plaintext (shown to user once) and as a JSON array of bcrypt hashes
// (stored in DB).
func GenerateRecoveryCodes() (plaintext []string, hashedJSON string, err error) {
	codes := make([]string, recoveryCodeCount)
	hashes := make([]string, recoveryCodeCount)

	for i := range codes {
		b := make([]byte, 5)
		if _, err := rand.Read(b); err != nil {
			return nil, "", fmt.Errorf("generating random bytes: %w", err)
		}
		codes[i] = strings.ToUpper(hex.EncodeToString(b))

		hash, err := bcrypt.GenerateFromPassword([]byte(codes[i]), bcrypt.DefaultCost)
		if err != nil {
			return nil, "", fmt.Errorf("hashing recovery code: %w", err)
		}
		hashes[i] = string(hash)
	}

	data, err := json.Marshal(hashes)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling hashes: %w", err)
	}
	return codes, string(data), nil
}

// ValidateRecoveryCode checks a recovery code against stored hashes.
// If valid, returns the updated hashes JSON with the used code removed.
// Returns false if no match.
func ValidateRecoveryCode(code, hashedJSON string) (bool, string) {
	if hashedJSON == "" {
		return false, hashedJSON
	}
	var hashes []string
	if err := json.Unmarshal([]byte(hashedJSON), &hashes); err != nil {
		return false, hashedJSON
	}

	code = strings.ToUpper(strings.TrimSpace(code))
	for i, h := range hashes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(code)) == nil {
			// Remove used code.
			hashes = append(hashes[:i], hashes[i+1:]...)
			data, _ := json.Marshal(hashes)
			return true, string(data)
		}
	}
	return false, hashedJSON
}
