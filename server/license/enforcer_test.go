package license

import (
	"context"
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

// setupEnforcerTest creates a temp dir with a valid license file and returns
// the enforcer, the temp dir path, and the public key hex.
func setupEnforcerTest(t *testing.T, payload Payload) (*Enforcer, string, ed25519.PrivateKey) {
	t.Helper()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubHex := hex.EncodeToString(pub)

	canonical, _ := CanonicalJSON(payload)
	sig := ed25519.Sign(priv, canonical)
	lic := &License{
		Payload:   payload,
		Signature: base64.StdEncoding.EncodeToString(sig),
	}

	dir := t.TempDir()
	licPath := filepath.Join(dir, "hobnob.lic")
	data, _ := json.MarshalIndent(lic, "", "  ")
	os.WriteFile(licPath, data, 0644)

	// Temporarily override the public key for verification.
	origPubKey := PublicKeyHex
	t.Cleanup(func() {
		// Restore (though it's a const, we use VerifyWithKey internally).
	})
	_ = origPubKey
	_ = pubHex

	return nil, dir, priv
}

func TestEnforcerCompliant(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-test",
		MaxThralls:   100,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}

	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	// Override the package-level public key constant via a custom verifier.
	// Since we can't change a const, we'll test through a different approach.
	// We'll create the enforcer and verify its behavior.

	// For testing, we need the public key embedded. Use a test-specific approach.
	origKey := PublicKeyHex
	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest(origKey) })

	counter := func(ctx context.Context) (int, error) { return 10, nil }
	shutdownCalled := false
	shutdown := func() { shutdownCalled = true }

	e, err := NewEnforcer(filepath.Join(dir, "hobnob.lic"), dir, counter, shutdown)
	if err != nil {
		t.Fatal(err)
	}

	if err := e.RegistrationAllowed(); err != nil {
		t.Fatalf("registration should be allowed: %v", err)
	}

	s := e.GetStatus()
	if s.Stage != StageCompliant {
		t.Errorf("expected Compliant, got %v", s.Stage)
	}
	if s.ActiveThralls != 10 {
		t.Errorf("expected 10 active thralls, got %d", s.ActiveThralls)
	}
	if shutdownCalled {
		t.Error("shutdown should not have been called")
	}
}

func TestEnforcerExpired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-test",
		MaxThralls:   100,
		IssuedAt:     "2020-01-01",
		ExpiresAt:    "2020-06-01", // expired
		Version:      1,
	}

	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	counter := func(ctx context.Context) (int, error) { return 10, nil }

	e, err := NewEnforcer(filepath.Join(dir, "hobnob.lic"), dir, counter, func() {})
	if err != nil {
		t.Fatal(err)
	}

	s := e.GetStatus()
	if s.Stage != StageGrace {
		t.Errorf("expected Grace, got %v", s.Stage)
	}
	if s.Violation == "" {
		t.Error("expected violation message")
	}
}

func TestEnforcerOverLimit(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-test",
		MaxThralls:   5,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}

	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	counter := func(ctx context.Context) (int, error) { return 10, nil } // 10 > 5

	e, err := NewEnforcer(filepath.Join(dir, "hobnob.lic"), dir, counter, func() {})
	if err != nil {
		t.Fatal(err)
	}

	s := e.GetStatus()
	if s.Stage != StageGrace {
		t.Errorf("expected Grace, got %v", s.Stage)
	}
}

func TestEnforcerNoLicenseFile(t *testing.T) {
	dir := t.TempDir()
	counter := func(ctx context.Context) (int, error) { return 0, nil }

	_, err := NewEnforcer(filepath.Join(dir, "nonexistent.lic"), dir, counter, func() {})
	if err == nil {
		t.Fatal("expected error for missing license file")
	}
}

func TestEnforcerReload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	// Start with expired license.
	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-test-1",
		MaxThralls:   100,
		IssuedAt:     "2020-01-01",
		ExpiresAt:    "2020-06-01",
		Version:      1,
	}

	dir := t.TempDir()
	licPath := filepath.Join(dir, "hobnob.lic")
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	counter := func(ctx context.Context) (int, error) { return 10, nil }

	e, err := NewEnforcer(licPath, dir, counter, func() {})
	if err != nil {
		t.Fatal(err)
	}

	if e.GetStatus().Stage != StageGrace {
		t.Fatal("expected grace stage with expired license")
	}

	// Replace with valid license.
	payload.ExpiresAt = time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02")
	payload.LicenseID = "lic-test-2"
	payload.Version = 2

	canonical, _ := CanonicalJSON(payload)
	sig := ed25519.Sign(priv, canonical)
	lic := &License{
		Payload:   payload,
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
	data, _ := json.MarshalIndent(lic, "", "  ")
	os.WriteFile(licPath, data, 0644)

	if err := e.Reload(); err != nil {
		t.Fatal(err)
	}

	if e.GetStatus().Stage != StageCompliant {
		t.Errorf("expected compliant after reload, got %v", e.GetStatus().Stage)
	}
}

func TestGraceStartPersistence(t *testing.T) {
	dir := t.TempDir()

	ts := time.Now().Add(-48 * time.Hour)
	writeGraceStart(dir, ts)

	read, err := readGraceStart(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Should be within 1 second of the written time.
	if read.Sub(ts).Abs() > time.Second {
		t.Errorf("grace start time mismatch: wrote %v, read %v", ts, read)
	}

	// Writing again should not overwrite (guards against clock reset).
	writeGraceStart(dir, time.Now())
	read2, _ := readGraceStart(dir)
	if read2.Sub(read).Abs() > time.Second {
		t.Error("grace_start file was overwritten — should be immutable once written")
	}

	// Clear should remove.
	clearGraceStart(dir)
	if _, err := readGraceStart(dir); err == nil {
		t.Error("expected error after clearing grace_start")
	}
}

// writeLic writes a signed license to dir/hobnob.lic.
func writeLic(t *testing.T, dir string, payload Payload, priv ed25519.PrivateKey) {
	t.Helper()
	canonical, _ := CanonicalJSON(payload)
	sig := ed25519.Sign(priv, canonical)
	lic := &License{
		Payload:   payload,
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
	data, _ := json.MarshalIndent(lic, "", "  ")
	os.WriteFile(filepath.Join(dir, "hobnob.lic"), data, 0644)
}
