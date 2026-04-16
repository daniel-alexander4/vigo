// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package license

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnforcerCompliant(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-test",
		Tier:         "standard",
		MaxEnvoys:   100,
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

	e, err := NewEnforcer(dir, dir, "", counter, shutdown)
	if err != nil {
		t.Fatal(err)
	}

	// Compliant, well below limit — allow.
	if err := e.RegistrationAllowedAt(10); err != nil {
		t.Fatalf("registration should be allowed at count=10/max=100: %v", err)
	}
	// Compliant, exactly one below limit — still allow.
	if err := e.RegistrationAllowedAt(99); err != nil {
		t.Fatalf("registration should be allowed at count=99/max=100: %v", err)
	}
	// Compliant, at limit — hard gate blocks.
	if err := e.RegistrationAllowedAt(100); err == nil {
		t.Fatal("expected hard-gate rejection at count=100/max=100")
	} else if !errors.Is(err, ErrNodeLimitReached) {
		t.Errorf("expected ErrNodeLimitReached, got %v", err)
	}
	// Compliant, over limit — hard gate blocks.
	if err := e.RegistrationAllowedAt(101); err == nil {
		t.Fatal("expected hard-gate rejection at count=101/max=100")
	} else if !errors.Is(err, ErrNodeLimitReached) {
		t.Errorf("expected ErrNodeLimitReached, got %v", err)
	}

	s := e.GetStatus()
	if s.Stage != StageCompliant {
		t.Errorf("expected Compliant, got %v", s.Stage)
	}
	if s.ActiveEnvoys != 10 {
		t.Errorf("expected 10 active envoys, got %d", s.ActiveEnvoys)
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
		Tier:         "standard",
		MaxEnvoys:   100,
		IssuedAt:     "2020-01-01",
		ExpiresAt:    "2020-06-01", // expired
		Version:      1,
	}

	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	counter := func(ctx context.Context) (int, error) { return 10, nil }

	e, err := NewEnforcer(dir, dir, "", counter, func() {})
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
		Tier:         "standard",
		MaxEnvoys:   5,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}

	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	counter := func(ctx context.Context) (int, error) { return 10, nil } // 10 > 5

	e, err := NewEnforcer(dir, dir, "", counter, func() {})
	if err != nil {
		t.Fatal(err)
	}

	s := e.GetStatus()
	if s.Stage != StageGrace {
		t.Errorf("expected Grace, got %v", s.Stage)
	}
}

func TestEnforcerNoLicenseFileFallsBackToFree(t *testing.T) {
	dir := t.TempDir()

	// Restore the real public key so the embedded free license verifies.
	origKey := PublicKeyHex
	setPublicKeyForTest("616d6b3f669392ffd165100387d799a429cec74c09f4da153a7543bce578d8d3")
	t.Cleanup(func() { setPublicKeyForTest(origKey) })

	counter := func(ctx context.Context) (int, error) { return 5, nil }

	e, err := NewEnforcer(dir, dir, "", counter, func() {})
	if err != nil {
		t.Fatalf("expected free license fallback, got error: %v", err)
	}

	s := e.GetStatus()
	if s.Stage != StageCompliant {
		t.Errorf("stage = %v, want Compliant", s.Stage)
	}
	if s.MaxEnvoys != 25 {
		t.Errorf("max_envoys = %d, want 25", s.MaxEnvoys)
	}
	if s.License.LicenseID != "free-25" {
		t.Errorf("license_id = %q, want %q", s.License.LicenseID, "free-25")
	}
}

func TestEnforcerNoLicenseAndNoKey(t *testing.T) {
	dir := t.TempDir()

	origKey := PublicKeyHex
	setPublicKeyForTest("")
	t.Cleanup(func() { setPublicKeyForTest(origKey) })

	counter := func(ctx context.Context) (int, error) { return 0, nil }

	_, err := NewEnforcer(dir, dir, "", counter, func() {})
	if err == nil {
		t.Fatal("expected error when no key and no license file")
	}
}

func TestEnforcerReload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	// Start with expired license.
	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-test-1",
		Tier:         "standard",
		MaxEnvoys:   100,
		IssuedAt:     "2020-01-01",
		ExpiresAt:    "2020-06-01",
		Version:      1,
	}

	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	counter := func(ctx context.Context) (int, error) { return 10, nil }

	e, err := NewEnforcer(dir, dir, "", counter, func() {})
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
	writeLic(t, dir, payload, priv)

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

func TestEnforcerTimeline(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-test",
		Tier:         "standard",
		MaxEnvoys:   5,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}

	dir := t.TempDir()
	graceDir := t.TempDir()
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	counter := func(ctx context.Context) (int, error) { return 10, nil }

	// Day 0 — grace period.
	e, err := NewEnforcer(dir, graceDir, "", counter, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if e.GetStatus().Stage != StageGrace {
		t.Fatalf("expected Grace at day 0, got %v", e.GetStatus().Stage)
	}

	// Simulate day 14 — still grace.
	clearGraceStart(graceDir)
	writeGraceStart(graceDir, time.Now().Add(-14*24*time.Hour))
	e.mu.Lock()
	e.graceStart = nil
	_ = e.evaluateLocked()
	e.mu.Unlock()
	if e.GetStatus().Stage != StageGrace {
		t.Errorf("expected Grace at day 14, got %v", e.GetStatus().Stage)
	}

	// Simulate day 16 — enrollment block.
	clearGraceStart(graceDir)
	writeGraceStart(graceDir, time.Now().Add(-16*24*time.Hour))
	e.mu.Lock()
	e.graceStart = nil
	_ = e.evaluateLocked()
	e.mu.Unlock()
	if e.GetStatus().Stage != StageEnrollBlock {
		t.Errorf("expected EnrollBlock at day 16, got %v", e.GetStatus().Stage)
	}
	// During StageEnrollBlock, the gate must refuse regardless of count.
	if err := e.RegistrationAllowedAt(0); err == nil {
		t.Error("registration should be blocked at StageEnrollBlock")
	} else if !errors.Is(err, ErrEnrollmentBlocked) {
		t.Errorf("expected ErrEnrollmentBlocked, got %v", err)
	}

	// Simulate day 31 — service stop.
	clearGraceStart(graceDir)
	writeGraceStart(graceDir, time.Now().Add(-31*24*time.Hour))
	e.mu.Lock()
	e.graceStart = nil
	err = e.evaluateLocked()
	e.mu.Unlock()
	if err == nil {
		t.Error("expected error at service stop stage")
	}
	if e.GetStatus().Stage != StageServiceStop {
		t.Errorf("expected ServiceStop at day 31, got %v", e.GetStatus().Stage)
	}
}

func TestCheckInWarning(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-test",
		Tier:         "standard",
		MaxEnvoys:   5,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}

	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	// Compliant — no warning.
	compliantCounter := func(ctx context.Context) (int, error) { return 3, nil }
	e, err := NewEnforcer(dir, dir, "", compliantCounter, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if w := e.CheckInWarning(); w != "" {
		t.Errorf("expected no warning when compliant, got %q", w)
	}

	// Over limit — warning.
	overCounter := func(ctx context.Context) (int, error) { return 10, nil }
	dir2 := t.TempDir()
	writeLic(t, dir2, payload, priv)
	e2, err := NewEnforcer(dir2, dir2, "", overCounter, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if w := e2.CheckInWarning(); w == "" {
		t.Error("expected warning when over limit")
	}
}

func TestIsFreeLicense(t *testing.T) {
	// With the built-in free license (no .lic on disk).
	dir := t.TempDir()
	origKey := PublicKeyHex
	setPublicKeyForTest("616d6b3f669392ffd165100387d799a429cec74c09f4da153a7543bce578d8d3")
	t.Cleanup(func() { setPublicKeyForTest(origKey) })

	counter := func(ctx context.Context) (int, error) { return 5, nil }
	e, err := NewEnforcer(dir, dir, "", counter, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if !e.IsFreeLicense() {
		t.Error("expected IsFreeLicense() = true for built-in free license")
	}

	// With a paid license.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	setPublicKeyForTest(hex.EncodeToString(pub))

	dir2 := t.TempDir()
	writeLic(t, dir2, Payload{
		CustomerName: "Paid Customer",
		CustomerID:   "paid-001",
		LicenseID:    "lic-paid-100",
		Tier:         "standard",
		MaxEnvoys:   100,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}, priv)

	e2, err := NewEnforcer(dir2, dir2, "", counter, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if e2.IsFreeLicense() {
		t.Error("expected IsFreeLicense() = false for paid license")
	}
}

// TestRegistrationAllowedAt_HardNodeLimit verifies the hard count gate
// rejects at count==max and count>max while compliant, without waiting for
// the staged enforcement timer. This is the primary regression guard for
// the "enrollments are rejected at the limit" contract.
func TestRegistrationAllowedAt_HardNodeLimit(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-hard-gate",
		Tier:         "standard",
		MaxEnvoys:    25,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}
	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	counter := func(ctx context.Context) (int, error) { return 10, nil }
	e, err := NewEnforcer(dir, dir, "", counter, func() {})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		count   int
		wantErr error
	}{
		{"empty fleet", 0, nil},
		{"well below", 10, nil},
		{"one below", 24, nil},
		{"at limit — hard gate", 25, ErrNodeLimitReached},
		{"one over", 26, ErrNodeLimitReached},
		{"far over", 50, ErrNodeLimitReached},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := e.RegistrationAllowedAt(tc.count)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("count=%d: unexpected error: %v", tc.count, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("count=%d: expected error, got nil", tc.count)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("count=%d: expected %v, got %v", tc.count, tc.wantErr, err)
			}
		})
	}
}

// TestRegistrationAllowedAt_DowngradeGraceStillBlocksHardLimit verifies that
// a post-install license downgrade (where the enforcer enters Stage 1 Grace
// because existing count > new max) still rejects new enrollments via the
// hard gate. The grace period gives existing envoys a runway to decommission,
// but no new envoys should be added during that runway.
func TestRegistrationAllowedAt_DowngradeGraceStillBlocksHardLimit(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	// 5-node license, fleet has 10 — mimics a downgrade.
	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-downgrade",
		Tier:         "standard",
		MaxEnvoys:    5,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}
	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	counter := func(ctx context.Context) (int, error) { return 10, nil }
	e, err := NewEnforcer(dir, dir, "", counter, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if e.GetStatus().Stage != StageGrace {
		t.Fatalf("expected Grace stage after downgrade, got %v", e.GetStatus().Stage)
	}

	// Hard gate rejects at any count >= max (5), regardless of grace.
	if err := e.RegistrationAllowedAt(10); err == nil {
		t.Error("expected rejection at count=10/max=5 during downgrade grace")
	}
	if err := e.RegistrationAllowedAt(5); err == nil {
		t.Error("expected rejection at count=5/max=5 during downgrade grace")
	}
	// Below max — allowed even during grace. The hard gate doesn't fire,
	// and Grace is not a blocking stage (it's a warning before
	// EnrollBlock).
	if err := e.RegistrationAllowedAt(4); err != nil {
		t.Errorf("expected allow at count=4/max=5 during downgrade grace (fleet is shrinking): %v", err)
	}
}

// TestRegistrationAllowedAt_StageServiceStopAlwaysBlocks verifies that
// StageServiceStop rejects regardless of the live count.
func TestRegistrationAllowedAt_StageServiceStopAlwaysBlocks(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-stop",
		Tier:         "standard",
		MaxEnvoys:    5,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}
	dir := t.TempDir()
	graceDir := t.TempDir()
	writeLic(t, dir, payload, priv)

	setPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { setPublicKeyForTest("") })

	counter := func(ctx context.Context) (int, error) { return 10, nil }
	e, err := NewEnforcer(dir, graceDir, "", counter, func() {})
	if err != nil {
		t.Fatal(err)
	}

	// Jump straight to day 31 → ServiceStop.
	clearGraceStart(graceDir)
	writeGraceStart(graceDir, time.Now().Add(-31*24*time.Hour))
	e.mu.Lock()
	e.graceStart = nil
	_ = e.evaluateLocked()
	e.mu.Unlock()
	if e.GetStatus().Stage != StageServiceStop {
		t.Fatalf("expected ServiceStop, got %v", e.GetStatus().Stage)
	}

	if err := e.RegistrationAllowedAt(0); err == nil {
		t.Error("ServiceStop should reject even with an empty fleet")
	} else if !errors.Is(err, ErrServiceStopping) {
		t.Errorf("expected ErrServiceStopping, got %v", err)
	}
}

// writeLic writes a signed license to dir/vigo.lic.
func writeLic(t *testing.T, dir string, payload Payload, priv ed25519.PrivateKey) {
	t.Helper()
	canonical, _ := CanonicalJSON(payload)
	sig := ed25519.Sign(priv, canonical)
	lic := &License{
		Payload:   payload,
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
	data, _ := json.MarshalIndent(lic, "", "  ")
	os.WriteFile(filepath.Join(dir, "vigo.lic"), data, 0644)
}
