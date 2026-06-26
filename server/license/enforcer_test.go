// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package license

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	licfmt "git.alexander4.io/tessera/license/products/vigo"
	"git.alexander4.io/vigo/server/metrics"
)

// realPublicKey captures `licfmt.PublicKeyHex` at package-init time —
// before any test mutates the global via `SetPublicKeyForTest`. Tests
// that need to assert against the embedded free license (which was
// signed at compile time with the real production key) use this
// instead of hardcoding a literal that goes stale on key rotations.
var realPublicKey = licfmt.PublicKeyHex

func TestEnforcerCompliant(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	payload := licfmt.Payload{
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
	origKey := licfmt.PublicKeyHex
	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(origKey) })

	counter := func(ctx context.Context) (int, error) { return 10, nil }
	shutdownCalled := false
	shutdown := func() { shutdownCalled = true }

	e, err := NewEnforcer(dir, dir, "", counter, shutdown, metrics.NewTestMetrics())
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

	payload := licfmt.Payload{
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

	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	counter := func(ctx context.Context) (int, error) { return 10, nil }

	e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
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

	payload := licfmt.Payload{
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

	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	counter := func(ctx context.Context) (int, error) { return 10, nil } // 10 > 5

	e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
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
	// `realPublicKey` is captured at package init before any other test
	// can mutate the global, so this is robust against test ordering.
	licfmt.SetPublicKeyForTest(realPublicKey)
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	counter := func(ctx context.Context) (int, error) { return 5, nil }

	e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
	if err != nil {
		t.Fatalf("expected free license fallback, got error: %v", err)
	}

	s := e.GetStatus()
	if s.Stage != StageCompliant {
		t.Errorf("stage = %v, want Compliant", s.Stage)
	}
	if s.MaxEnvoys != 100 {
		t.Errorf("max_envoys = %d, want 100", s.MaxEnvoys)
	}
	if s.License.LicenseID != "free-100" {
		t.Errorf("license_id = %q, want %q", s.License.LicenseID, "free-100")
	}
}

func TestEnforcerNoLicenseAndNoKey(t *testing.T) {
	dir := t.TempDir()

	licfmt.SetPublicKeyForTest("")
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	counter := func(ctx context.Context) (int, error) { return 0, nil }

	_, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
	if err == nil {
		t.Fatal("expected error when no key and no license file")
	}
}

func TestEnforcerPaidTierRequiresFingerprint(t *testing.T) {
	// A paid-tier license without a fingerprint must be rejected at
	// install — gap 1 of the v0.67.15 license-process review. The
	// enforcer routes this through the same staged-enforcement path
	// the fingerprint-mismatch case uses: violation message set,
	// stage advances to Grace, graceStart pinned to "now."
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	// Starter is the smallest paid tier (101-250 nodes per tessera's
	// pricing table). RequiresBinding("starter") must return true.
	payload := licfmt.Payload{
		CustomerName: "Paid Customer",
		CustomerID:   "paid-001",
		LicenseID:    "lic-paid-no-fp",
		Tier:         "starter",
		MaxEnvoys:    200,
		IssuedAt:     "2026-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
		// Fingerprint deliberately empty.
	}

	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	counter := func(ctx context.Context) (int, error) { return 0, nil }
	e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	s := e.GetStatus()
	if s.Violation == "" {
		t.Fatal("expected non-empty violation for paid tier with no fingerprint")
	}
	if s.Stage != StageGrace {
		t.Errorf("expected StageGrace, got %v", s.Stage)
	}
	// Operator should see the canonical fix path in the message.
	for _, want := range []string{"fingerprint", "starter", "vigocli license fingerprint"} {
		if !strings.Contains(s.Violation, want) {
			t.Errorf("violation message %q must contain %q", s.Violation, want)
		}
	}
}

func TestEnforcerCommunityTierWithoutFingerprintInstalls(t *testing.T) {
	// Community (free) tier without a fingerprint is the canonical
	// install path — must reach Compliant, no violation.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	payload := licfmt.Payload{
		CustomerName: "Free User",
		CustomerID:   "free-001",
		LicenseID:    "lic-free",
		Tier:         "community",
		MaxEnvoys:    100,
		IssuedAt:     "2026-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}

	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	counter := func(ctx context.Context) (int, error) { return 10, nil }
	e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	s := e.GetStatus()
	if s.Stage != StageCompliant {
		t.Errorf("community tier without fingerprint should be Compliant, got %v (violation=%q)", s.Stage, s.Violation)
	}
}

func TestEnforcerReload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	// Start with expired license.
	payload := licfmt.Payload{
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

	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	counter := func(ctx context.Context) (int, error) { return 10, nil }

	e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
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

	payload := licfmt.Payload{
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

	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	counter := func(ctx context.Context) (int, error) { return 10, nil }

	// Day 0 — grace period.
	e, err := NewEnforcer(dir, graceDir, "", counter, func() {}, metrics.NewTestMetrics())
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

	payload := licfmt.Payload{
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

	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	// Compliant — no warning.
	compliantCounter := func(ctx context.Context) (int, error) { return 3, nil }
	e, err := NewEnforcer(dir, dir, "", compliantCounter, func() {}, metrics.NewTestMetrics())
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
	e2, err := NewEnforcer(dir2, dir2, "", overCounter, func() {}, metrics.NewTestMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if w := e2.CheckInWarning(); w == "" {
		t.Error("expected warning when over limit")
	}
}

func TestIsCommercialLicense(t *testing.T) {
	counter := func(ctx context.Context) (int, error) { return 5, nil }

	// Built-in free license (no .lic on disk) is community tier — not
	// commercial. Restore the real production pubkey (captured at package
	// init): the embedded free license was signed with it at compile time.
	licfmt.SetPublicKeyForTest(realPublicKey)
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })
	builtin, err := NewEnforcer(t.TempDir(), t.TempDir(), "", counter, func() {}, metrics.NewTestMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if builtin.IsCommercialLicense() {
		t.Error("built-in free license must not be commercial")
	}

	// Signed licenses below are verified against a per-test key.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))

	cases := []struct {
		name       string
		tier       string
		licenseID  string
		maxEnvoys  int
		commercial bool
	}{
		// A hand-signed community license with a huge node count and a
		// non-"free-100" id (the gcloud/loadtest-unlimited.lic shape) is
		// still non-commercial — the gap the old license_id check missed.
		{"community-unlimited", "community", "free-100000000", 100_000_000, false},
		// The legacy display alias is not a paid bracket.
		{"legacy-standard", "standard", "lic-legacy", 100, false},
		{"paid-starter", "starter", "lic-starter", 200, true},
		{"paid-scale", "scale", "lic-scale", 50_000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeLic(t, dir, licfmt.Payload{
				CustomerName: "Test",
				CustomerID:   "test",
				LicenseID:    tc.licenseID,
				Tier:         tc.tier,
				MaxEnvoys:    tc.maxEnvoys,
				IssuedAt:     "2025-01-01",
				ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
				Version:      1,
			}, priv)
			e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
			if err != nil {
				t.Fatal(err)
			}
			if got := e.IsCommercialLicense(); got != tc.commercial {
				t.Errorf("IsCommercialLicense() = %v, want %v for tier %q", got, tc.commercial, tc.tier)
			}
		})
	}
}

// TestRegistrationAllowedAt_HardNodeLimit verifies the hard count gate
// rejects at count==max and count>max while compliant, without waiting for
// the staged enforcement timer. This is the primary regression guard for
// the "enrollments are rejected at the limit" contract.
func TestRegistrationAllowedAt_HardNodeLimit(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := licfmt.Payload{
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

	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	counter := func(ctx context.Context) (int, error) { return 10, nil }
	e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
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

// TestCurrentCount_DelegatesToCounter verifies the enforcer surfaces its
// injected mode-aware counter through CurrentCount — the value the enrollment
// gate reads before RegistrationAllowedAt, so the gate enforces against the
// licensed-count source of truth rather than a separate local count. Both the
// success and the error pass-through are covered.
func TestCurrentCount_DelegatesToCounter(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := licfmt.Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-current-count",
		Tier:         "standard",
		MaxEnvoys:    50,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}
	dir := t.TempDir()
	writeLic(t, dir, payload, priv)
	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	t.Run("returns the counter value", func(t *testing.T) {
		counter := func(ctx context.Context) (int, error) { return 37, nil }
		e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.CurrentCount(context.Background())
		if err != nil {
			t.Fatalf("CurrentCount: %v", err)
		}
		if got != 37 {
			t.Errorf("CurrentCount = %d, want 37", got)
		}
	})

	t.Run("propagates the counter error", func(t *testing.T) {
		wantErr := errors.New("count source unavailable")
		counter := func(ctx context.Context) (int, error) { return 0, wantErr }
		e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.CurrentCount(context.Background()); !errors.Is(err, wantErr) {
			t.Errorf("CurrentCount err = %v, want %v", err, wantErr)
		}
	})
}

// TestRegistrationAllowedAt_DowngradeGraceStillBlocksHardLimit verifies that
// a post-install license downgrade (where the enforcer enters Stage 1 Grace
// because existing count > new max) still rejects new enrollments via the
// hard gate. The grace period gives existing envoys a runway to decommission,
// but no new envoys should be added during that runway.
func TestRegistrationAllowedAt_DowngradeGraceStillBlocksHardLimit(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	// 5-node license, fleet has 10 — mimics a downgrade.
	payload := licfmt.Payload{
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

	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	counter := func(ctx context.Context) (int, error) { return 10, nil }
	e, err := NewEnforcer(dir, dir, "", counter, func() {}, metrics.NewTestMetrics())
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
	payload := licfmt.Payload{
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

	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(realPublicKey) })

	counter := func(ctx context.Context) (int, error) { return 10, nil }
	e, err := NewEnforcer(dir, graceDir, "", counter, func() {}, metrics.NewTestMetrics())
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

// TestEnforcerRefreshMetrics verifies RefreshMetrics re-reads the live count
// from the counter and re-sets the vigo_license_envoys_used gauge. This is the
// path that keeps the gauge fresh on the metrics-collector cadence after a bulk
// envoy deletion, instead of lagging until the hourly enforcement tick.
func TestEnforcerRefreshMetrics(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	payload := licfmt.Payload{
		CustomerName: "Test",
		CustomerID:   "test-001",
		LicenseID:    "lic-test",
		Tier:         "standard",
		MaxEnvoys:    100,
		IssuedAt:     "2025-01-01",
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02"),
		Version:      1,
	}

	dir := t.TempDir()
	writeLic(t, dir, payload, priv)

	origKey := licfmt.PublicKeyHex
	licfmt.SetPublicKeyForTest(hex.EncodeToString(pub))
	t.Cleanup(func() { licfmt.SetPublicKeyForTest(origKey) })

	// A mutable counter so we can simulate the fleet count dropping out from
	// under a previously-set gauge (the bulk-delete scenario).
	count := 90
	counter := func(ctx context.Context) (int, error) { return count, nil }

	m := metrics.NewTestMetrics()
	e, err := NewEnforcer(dir, dir, "", counter, func() {}, m)
	if err != nil {
		t.Fatal(err)
	}

	// NewEnforcer's initial evaluation set the gauge to the starting count.
	if got := testutil.ToFloat64(m.LicenseEnvoysUsed); got != 90 {
		t.Fatalf("initial LicenseEnvoysUsed = %v, want 90", got)
	}

	// Fleet shrinks (bulk delete). Without a refresh the gauge would stay at
	// 90 until the hourly tick; RefreshMetrics pulls it current immediately.
	count = 5
	e.RefreshMetrics()

	if got := testutil.ToFloat64(m.LicenseEnvoysUsed); got != 5 {
		t.Fatalf("after RefreshMetrics LicenseEnvoysUsed = %v, want 5", got)
	}
}

// writeLic writes a signed license to dir/vigo.lic.
func writeLic(t *testing.T, dir string, payload licfmt.Payload, priv ed25519.PrivateKey) {
	t.Helper()
	lic, err := licfmt.Sign(payload, priv)
	if err != nil {
		t.Fatalf("signing license: %v", err)
	}
	data, _ := json.MarshalIndent(lic, "", "  ")
	os.WriteFile(filepath.Join(dir, "vigo.lic"), data, 0644)
}
