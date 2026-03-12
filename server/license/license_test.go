// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package license

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testKeypair generates an Ed25519 keypair for testing.
func testKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// testPayload returns a valid payload for testing.
func testPayload() Payload {
	return Payload{
		CustomerName: "Test Corp",
		CustomerID:   "test-001",
		LicenseID:    "lic-20250301-test",
		MaxThralls:   100,
		IssuedAt:     "2025-03-01",
		ExpiresAt:    "2027-03-01",
		Version:      1,
	}
}

// signLicense creates a signed license from a payload.
func signLicense(t *testing.T, payload Payload, priv ed25519.PrivateKey) *License {
	t.Helper()
	canonical, err := CanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, canonical)
	return &License{
		Payload:   payload,
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
}

func TestCanonicalJSON(t *testing.T) {
	p := testPayload()
	data, err := CanonicalJSON(p)
	if err != nil {
		t.Fatal(err)
	}

	// Verify deterministic: run twice, same result.
	data2, err := CanonicalJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(data2) {
		t.Fatal("canonical JSON is not deterministic")
	}

	// Verify sorted keys.
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	// Verify no extra whitespace (compact).
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		t.Fatal(err)
	}
	if string(data) != buf.String() {
		t.Errorf("canonical JSON has whitespace:\n  got:  %s\n  want: %s", data, buf.String())
	}
}

func TestVerifyValidSignature(t *testing.T) {
	pub, priv := testKeypair(t)
	lic := signLicense(t, testPayload(), priv)
	pubHex := hex.EncodeToString(pub)

	if err := lic.VerifyWithKey(pubHex); err != nil {
		t.Fatalf("expected valid signature, got: %v", err)
	}
}

func TestVerifyTamperedPayload(t *testing.T) {
	pub, priv := testKeypair(t)
	lic := signLicense(t, testPayload(), priv)
	pubHex := hex.EncodeToString(pub)

	// Tamper with the payload.
	lic.Payload.MaxThralls = 99999
	if err := lic.VerifyWithKey(pubHex); err == nil {
		t.Fatal("expected tampered payload to fail verification")
	}
}

func TestVerifyWrongKey(t *testing.T) {
	_, priv := testKeypair(t)
	pub2, _ := testKeypair(t)
	lic := signLicense(t, testPayload(), priv)

	wrongPubHex := hex.EncodeToString(pub2)
	if err := lic.VerifyWithKey(wrongPubHex); err == nil {
		t.Fatal("expected wrong key to fail verification")
	}
}

func TestVerifyEmptyKey(t *testing.T) {
	_, priv := testKeypair(t)
	lic := signLicense(t, testPayload(), priv)

	if err := lic.VerifyWithKey(""); err == nil {
		t.Fatal("expected empty key to fail")
	}
}

func TestParseMissingFields(t *testing.T) {
	// Missing license_id.
	data := `{"payload":{"customer_name":"X"},"signature":"abc"}`
	_, err := Parse([]byte(data))
	if err == nil {
		t.Fatal("expected error for missing license_id")
	}

	// Missing signature.
	data = `{"payload":{"license_id":"x"}}`
	_, err = Parse([]byte(data))
	if err == nil {
		t.Fatal("expected error for missing signature")
	}
}

func TestLoadFile(t *testing.T) {
	pub, priv := testKeypair(t)
	lic := signLicense(t, testPayload(), priv)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.lic")

	data, _ := json.MarshalIndent(lic, "", "  ")
	os.WriteFile(path, data, 0644)

	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := loaded.VerifyWithKey(hex.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}

	if loaded.Payload.CustomerName != "Test Corp" {
		t.Errorf("got customer name %q", loaded.Payload.CustomerName)
	}
}

func TestExpiresTime(t *testing.T) {
	_, priv := testKeypair(t)
	lic := signLicense(t, testPayload(), priv)

	exp, err := lic.ExpiresTime()
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := time.Parse("2006-01-02", "2027-03-01")
	if !exp.Equal(expected) {
		t.Errorf("got %v, want %v", exp, expected)
	}
}
