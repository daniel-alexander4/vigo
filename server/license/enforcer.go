// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package license

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/daniel-alexander4/vigo/server/metrics"
)

// Stage represents the current license enforcement stage.
type Stage int

const (
	StageCompliant  Stage = 0
	StageGrace      Stage = 1
	StageEnrollBlock Stage = 2
	StageServiceStop Stage = 3
)

func (s Stage) String() string {
	switch s {
	case StageCompliant:
		return "Compliant"
	case StageGrace:
		return "Grace Period"
	case StageEnrollBlock:
		return "Enrollment Block"
	case StageServiceStop:
		return "Service Stop"
	default:
		return fmt.Sprintf("Unknown(%d)", int(s))
	}
}

const (
	graceDays       = 15
	enrollBlockDays = 15
	totalDaysToStop = graceDays + enrollBlockDays // 30
)

// EnvoyCounter returns the current number of active (non-revoked) envoys.
type EnvoyCounter func(ctx context.Context) (int, error)

// ShutdownFunc is called when the enforcer reaches Stage 3.
type ShutdownFunc func()

// Status holds the current license enforcement state.
type Status struct {
	Stage          Stage     `json:"stage"`
	License        *Payload  `json:"license,omitempty"`
	ActiveEnvoys  int       `json:"active_envoys"`
	MaxEnvoys     int       `json:"max_envoys"`
	ExpiresAt      string    `json:"expires_at,omitempty"`
	DaysRemaining  int       `json:"days_remaining,omitempty"`
	GraceStartedAt string    `json:"grace_started_at,omitempty"`
	GraceDaysLeft  int       `json:"grace_days_left,omitempty"`
	Violation      string    `json:"violation,omitempty"`
}

// Enforcer manages license validation and enforcement at runtime.
type Enforcer struct {
	mu           sync.RWMutex
	license      *License
	licenseDir   string
	graceDir     string
	caFile       string // TLS CA cert path for fingerprint computation
	counter      EnvoyCounter
	shutdownFn   ShutdownFunc
	stage        Stage
	graceStart   *time.Time
	violation    string
	lastLogTime  time.Time
}

// NewEnforcer creates an enforcer that scans licenseDir for valid .lic files.
// It validates the license immediately and returns an error if no valid
// license is found or the license is at Stage 3 (service stop).
// caFile is the path to the TLS CA cert (used for machine fingerprint validation).
func NewEnforcer(licenseDir, graceDir, caFile string, counter EnvoyCounter, shutdownFn ShutdownFunc) (*Enforcer, error) {
	e := &Enforcer{
		licenseDir: licenseDir,
		graceDir:   graceDir,
		caFile:     caFile,
		counter:    counter,
		shutdownFn: shutdownFn,
	}

	if err := e.loadAndCheck(); err != nil {
		return nil, err
	}

	return e, nil
}

// Reload re-reads the license file from disk and re-evaluates enforcement.
// Called on SIGHUP.
func (e *Enforcer) Reload() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loadAndCheckLocked()
}

// RegistrationAllowed returns nil if new envoy registrations are permitted,
// or an error describing why they are blocked.
func (e *Enforcer) RegistrationAllowed() error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	switch e.stage {
	case StageEnrollBlock:
		return fmt.Errorf("license violation: new enrollments are blocked (enforcement stage 2 — enrollment block)")
	case StageServiceStop:
		return fmt.Errorf("license violation: server is shutting down (enforcement stage 3 — service stop)")
	default:
		return nil
	}
}

// GetStatus returns the current enforcement status for the API.
func (e *Enforcer) GetStatus() Status {
	e.mu.RLock()
	defer e.mu.RUnlock()

	s := Status{
		Stage: e.stage,
	}

	if e.license != nil {
		p := e.license.Payload
		s.License = &p
		s.MaxEnvoys = p.MaxEnvoys
		s.ExpiresAt = p.ExpiresAt

		if exp, err := e.license.ExpiresTime(); err == nil {
			days := int(time.Until(exp).Hours() / 24)
			s.DaysRemaining = days
		}
	}

	if count, err := e.counter(context.Background()); err == nil {
		s.ActiveEnvoys = count
	}

	if e.graceStart != nil {
		s.GraceStartedAt = e.graceStart.Format(time.RFC3339)
		elapsed := int(time.Since(*e.graceStart).Hours() / 24)
		s.GraceDaysLeft = totalDaysToStop - elapsed
		if s.GraceDaysLeft < 0 {
			s.GraceDaysLeft = 0
		}
	}

	s.Violation = e.violation
	return s
}

// Run starts the periodic enforcement check loop. It logs warnings/errors
// at the cadence defined by each stage and triggers shutdown at Stage 3.
func (e *Enforcer) Run(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.mu.Lock()
			_ = e.loadAndCheckLocked()
			e.logIfNeeded()
			if e.stage == StageServiceStop && e.shutdownFn != nil {
				e.mu.Unlock()
				slog.Error("license enforcement: stage 3 reached — initiating server shutdown",
					"license_id", e.license.Payload.LicenseID,
					"violation", e.violation)
				e.shutdownFn()
				return
			}
			e.mu.Unlock()
		}
	}
}

// loadAndCheck loads the license and evaluates enforcement. Acquires lock.
func (e *Enforcer) loadAndCheck() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loadAndCheckLocked()
}

// loadAndCheckLocked does the work without acquiring the lock.
func (e *Enforcer) loadAndCheckLocked() error {
	lic, path, err := LoadDir(e.licenseDir)
	if err != nil {
		return fmt.Errorf("%w\nPlace a .lic file in %s or install with: vigogo license install --file=<path>", err, e.licenseDir)
	}

	slog.Info("license loaded", "path", path)
	e.license = lic

	// Validate machine fingerprint for paid licenses.
	// Free tier (no fingerprint) is always accepted.
	if lic.Payload.Fingerprint != "" {
		actual, fpErr := MachineFingerprint(e.caFile)
		if fpErr != nil {
			slog.Warn("license fingerprint check: could not compute machine fingerprint", "error", fpErr)
		} else if actual != lic.Payload.Fingerprint {
			slog.Warn("license fingerprint mismatch — license is bound to a different machine",
				"expected", lic.Payload.Fingerprint[:12]+"...",
				"actual", actual[:12]+"...",
			)
			e.violation = "license fingerprint mismatch — this license is bound to a different machine. Contact support for a new license."
			e.stage = StageGrace
			if e.graceStart == nil {
				now := time.Now()
				e.graceStart = &now
				writeGraceStart(e.graceDir, now)
			}
			return nil
		}
	}

	return e.evaluateLocked()
}

// evaluateLocked determines the current enforcement stage.
func (e *Enforcer) evaluateLocked() error {
	now := time.Now()

	expired := false
	if exp, err := e.license.ExpiresTime(); err == nil {
		expired = now.After(exp)
	}

	overLimit := false
	if count, err := e.counter(context.Background()); err == nil {
		overLimit = count > e.license.Payload.MaxEnvoys
	}

	if !expired && !overLimit {
		// Compliant — clear grace state.
		e.stage = StageCompliant
		e.graceStart = nil
		e.violation = ""
		clearGraceStart(e.graceDir)
		e.updateMetrics()
		return nil
	}

	// Determine violation reason.
	if expired && overLimit {
		e.violation = "license expired and envoy count exceeds limit"
	} else if expired {
		e.violation = "license expired"
	} else {
		e.violation = fmt.Sprintf("active envoys exceed license limit (%d)", e.license.Payload.MaxEnvoys)
	}

	// Load or create grace start timestamp.
	if e.graceStart == nil {
		if gs, err := readGraceStart(e.graceDir); err == nil {
			e.graceStart = &gs
		} else {
			t := now
			e.graceStart = &t
			writeGraceStart(e.graceDir, t)
		}
	}

	elapsed := int(now.Sub(*e.graceStart).Hours() / 24)

	switch {
	case elapsed >= totalDaysToStop:
		e.stage = StageServiceStop
		e.updateMetrics()
		return fmt.Errorf("license enforcement stage 3: %s — server must shut down (day %d of %d)", e.violation, elapsed, totalDaysToStop)
	case elapsed >= graceDays:
		e.stage = StageEnrollBlock
	default:
		e.stage = StageGrace
	}

	e.updateMetrics()
	return nil
}

// updateMetrics sets Prometheus gauges for current license state.
// Must be called with e.mu held.
func (e *Enforcer) updateMetrics() {
	metrics.LicenseStage.Set(float64(e.stage))
	if e.license != nil {
		metrics.LicenseEnvoysMax.Set(float64(e.license.Payload.MaxEnvoys))
		if exp, err := e.license.ExpiresTime(); err == nil {
			days := int(time.Until(exp).Hours() / 24)
			metrics.LicenseDaysRemaining.Set(float64(days))
			metrics.LicenseExpiryTimestamp.Set(float64(exp.Unix()))
		}
	}
	if count, err := e.counter(context.Background()); err == nil {
		metrics.LicenseEnvoysUsed.Set(float64(count))
	}
}

// FreeLicenseID is the license_id of the built-in 25-node community license.
const FreeLicenseID = "free-25"

// IsFreeLicense returns true if the loaded license is the built-in free tier.
func (e *Enforcer) IsFreeLicense() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.license != nil && e.license.Payload.LicenseID == FreeLicenseID
}

// CheckInWarning returns a non-empty warning message if the license is in
// grace period or enrollment block. The check-in handler should log this on
// every check-in so operators see it in their logs.
func (e *Enforcer) CheckInWarning() string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.stage == StageCompliant || e.stage == StageServiceStop {
		return ""
	}

	elapsed := 0
	if e.graceStart != nil {
		elapsed = int(time.Since(*e.graceStart).Hours() / 24)
	}

	switch e.stage {
	case StageGrace:
		return fmt.Sprintf("license violation: %s (grace period day %d of %d — reduce node count or upgrade license)", e.violation, elapsed, graceDays)
	case StageEnrollBlock:
		return fmt.Sprintf("license violation: %s (enrollment blocked, day %d of %d — server shutdown in %d days)", e.violation, elapsed, totalDaysToStop, totalDaysToStop-elapsed)
	}
	return ""
}

// logIfNeeded emits log messages at the appropriate cadence per stage.
func (e *Enforcer) logIfNeeded() {
	now := time.Now()
	var interval time.Duration

	switch e.stage {
	case StageCompliant:
		return
	case StageGrace:
		interval = 24 * time.Hour
	case StageEnrollBlock, StageServiceStop:
		interval = 1 * time.Hour
	}

	if now.Sub(e.lastLogTime) < interval {
		return
	}
	e.lastLogTime = now

	elapsed := 0
	if e.graceStart != nil {
		elapsed = int(now.Sub(*e.graceStart).Hours() / 24)
	}

	switch e.stage {
	case StageGrace:
		slog.Warn("license violation: grace period active",
			"violation", e.violation,
			"license_id", e.license.Payload.LicenseID,
			"day", elapsed,
			"grace_days_remaining", graceDays-elapsed)
	case StageEnrollBlock:
		slog.Error("license violation: new enrollments blocked",
			"violation", e.violation,
			"license_id", e.license.Payload.LicenseID,
			"day", elapsed,
			"days_until_service_stop", totalDaysToStop-elapsed)
	}
}
