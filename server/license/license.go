// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const DefaultLicenseDir = "/srv/hobnob/license"

// Payload contains the license terms. Field order does not matter for
// verification because we canonicalize before signing/verifying.
type Payload struct {
	CustomerName string `json:"customer_name"`
	CustomerID   string `json:"customer_id"`
	LicenseID    string `json:"license_id"`
	Tier         string `json:"tier"` // "free", "standard", "growth", "enterprise"
	MaxThralls   int    `json:"max_thralls"`
	IssuedAt     string `json:"issued_at"`
	ExpiresAt    string `json:"expires_at"`
	Version      int    `json:"version"`
}

// TierDisplayName returns a human-readable name for the tier.
func (p *Payload) TierDisplayName() string {
	switch p.Tier {
	case "free":
		return "Free (25 nodes)"
	case "standard":
		return "Standard"
	case "growth":
		return "Growth"
	case "enterprise":
		return "Enterprise"
	default:
		return p.Tier
	}
}

// License is the on-disk format: a payload plus its Ed25519 signature.
type License struct {
	Payload   Payload `json:"payload"`
	Signature string  `json:"signature"` // base64-encoded
}

// LoadFile reads and parses a license file from disk.
func LoadFile(path string) (*License, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading license file: %w", err)
	}
	return Parse(data)
}

// LoadDir scans a directory for .lic files and returns the first one that
// parses and passes signature verification. Files are tried in lexicographic
// order. If no valid .lic file is found on disk, falls back to the embedded
// 25-node community license.
func LoadDir(dir string) (*License, string, error) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.lic"))
	sort.Strings(matches)

	for _, path := range matches {
		lic, err := LoadFile(path)
		if err != nil {
			continue
		}
		if lic.Verify() != nil {
			continue
		}
		return lic, path, nil
	}

	// No valid .lic on disk — use the built-in free license.
	lic, err := FreeLicense()
	if err != nil {
		return nil, "", fmt.Errorf("no valid license on disk and embedded free license failed: %w", err)
	}
	return lic, "(built-in free-25)", nil
}

// Parse decodes a license from raw JSON bytes.
func Parse(data []byte) (*License, error) {
	var lic License
	if err := json.Unmarshal(data, &lic); err != nil {
		return nil, fmt.Errorf("parsing license JSON: %w", err)
	}
	if lic.Payload.LicenseID == "" {
		return nil, fmt.Errorf("license file missing license_id")
	}
	if lic.Payload.Tier == "" {
		return nil, fmt.Errorf("license file missing tier")
	}
	if lic.Signature == "" {
		return nil, fmt.Errorf("license file missing signature")
	}
	return &lic, nil
}

// Verify checks the Ed25519 signature against the embedded public key.
// Returns an error if the signature is invalid or the public key is not configured.
func (l *License) Verify() error {
	return l.VerifyWithKey(PublicKeyHex)
}

// VerifyWithKey checks the signature against the given hex-encoded public key.
func (l *License) VerifyWithKey(pubKeyHex string) error {
	if pubKeyHex == "" {
		return fmt.Errorf("no public key configured — rebuild with hobnob-license-manager keys inject")
	}

	pubBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return fmt.Errorf("decoding public key hex: %w", err)
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key size: got %d, want %d", len(pubBytes), ed25519.PublicKeySize)
	}
	pubKey := ed25519.PublicKey(pubBytes)

	canonical, err := CanonicalJSON(l.Payload)
	if err != nil {
		return fmt.Errorf("canonicalizing payload: %w", err)
	}

	sig, err := base64.StdEncoding.DecodeString(l.Signature)
	if err != nil {
		return fmt.Errorf("decoding signature: %w", err)
	}

	if !ed25519.Verify(pubKey, canonical, sig) {
		return fmt.Errorf("license signature verification failed — file may be tampered")
	}
	return nil
}

// ExpiresTime parses the expires_at field as a date.
func (l *License) ExpiresTime() (time.Time, error) {
	return time.Parse("2006-01-02", l.Payload.ExpiresAt)
}

// IssuedTime parses the issued_at field as a date.
func (l *License) IssuedTime() (time.Time, error) {
	return time.Parse("2006-01-02", l.Payload.IssuedAt)
}

// CanonicalJSON produces a deterministic JSON encoding of a value by sorting
// object keys and removing whitespace. This is the byte sequence that gets
// signed and verified.
func CanonicalJSON(v any) ([]byte, error) {
	// First marshal to JSON to get a map we can sort.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	// Unmarshal into an ordered structure and re-marshal sorted.
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return marshalSorted(m)
}

// marshalSorted produces deterministic JSON with sorted keys.
func marshalSorted(m map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf := []byte("{")
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		keyJSON, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf = append(buf, keyJSON...)
		buf = append(buf, ':')

		valJSON, err := json.Marshal(m[k])
		if err != nil {
			return nil, err
		}
		buf = append(buf, valJSON...)
	}
	buf = append(buf, '}')
	return buf, nil
}
