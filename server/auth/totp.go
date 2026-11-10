// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

// TOTPIssuer is the issuer name shown in authenticator apps.
const TOTPIssuer = "Vigo"

// totpPeriod is the RFC 6238 time-step the default validator uses (30s).
const totpPeriod = 30

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

// ValidateTOTPCodeStep checks a code against the secret and, if valid, returns
// the time-step it matched (unix/period). It mirrors the default validator's
// ±1 skew window so callers can record the consumed step and reject replays
// within the validity window. Returns ok=false, step=0 when no step matches.
func ValidateTOTPCodeStep(code, secret string, now time.Time) (ok bool, step int64) {
	for skew := int64(-1); skew <= 1; skew++ {
		t := now.Add(time.Duration(skew*totpPeriod) * time.Second)
		expected, err := totp.GenerateCode(secret, t)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return true, t.Unix() / totpPeriod
		}
	}
	return false, 0
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
