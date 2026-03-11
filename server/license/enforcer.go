package license

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Stage represents the current license enforcement stage.
type Stage int

const (
	StageCompliant  Stage = 0
	StageGrace      Stage = 1
	StageThrallBlock Stage = 2
	StageServiceStop Stage = 3
)

func (s Stage) String() string {
	switch s {
	case StageCompliant:
		return "Compliant"
	case StageGrace:
		return "Grace Period"
	case StageThrallBlock:
		return "Thrall Block"
	case StageServiceStop:
		return "Service Stop"
	default:
		return fmt.Sprintf("Unknown(%d)", int(s))
	}
}

const (
	graceDays      = 30
	thrallBlockDays = 15
	totalDaysToStop = graceDays + thrallBlockDays // 45
)

// ThrallCounter returns the current number of active (non-revoked) thralls.
type ThrallCounter func(ctx context.Context) (int, error)

// ShutdownFunc is called when the enforcer reaches Stage 3.
type ShutdownFunc func()

// Status holds the current license enforcement state.
type Status struct {
	Stage          Stage     `json:"stage"`
	License        *Payload  `json:"license,omitempty"`
	ActiveThralls  int       `json:"active_thralls"`
	MaxThralls     int       `json:"max_thralls"`
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
	licensePath  string
	graceDir     string
	counter      ThrallCounter
	shutdownFn   ShutdownFunc
	stage        Stage
	graceStart   *time.Time
	violation    string
	lastLogTime  time.Time
}

// NewEnforcer creates an enforcer that loads the license from the given path.
// It validates the license immediately and returns an error if the license
// is missing, invalid, or at Stage 3 (service stop).
func NewEnforcer(licensePath, graceDir string, counter ThrallCounter, shutdownFn ShutdownFunc) (*Enforcer, error) {
	e := &Enforcer{
		licensePath: licensePath,
		graceDir:    graceDir,
		counter:     counter,
		shutdownFn:  shutdownFn,
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

// RegistrationAllowed returns nil if new thrall registrations are permitted,
// or an error describing why they are blocked.
func (e *Enforcer) RegistrationAllowed() error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	switch e.stage {
	case StageThrallBlock:
		return fmt.Errorf("license violation: new thrall registrations are blocked (enforcement stage 2 — thrall block)")
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
		s.MaxThralls = p.MaxThralls
		s.ExpiresAt = p.ExpiresAt

		if exp, err := e.license.ExpiresTime(); err == nil {
			days := int(time.Until(exp).Hours() / 24)
			s.DaysRemaining = days
		}
	}

	if count, err := e.counter(context.Background()); err == nil {
		s.ActiveThralls = count
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
	lic, err := LoadFile(e.licensePath)
	if err != nil {
		return fmt.Errorf("no valid license file at %s: %w\nInstall a license with: hobnobcli license install --file=<path>", e.licensePath, err)
	}

	if err := lic.Verify(); err != nil {
		return fmt.Errorf("license verification failed: %w", err)
	}

	e.license = lic
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
		overLimit = count > e.license.Payload.MaxThralls
	}

	if !expired && !overLimit {
		// Compliant — clear grace state.
		e.stage = StageCompliant
		e.graceStart = nil
		e.violation = ""
		clearGraceStart(e.graceDir)
		return nil
	}

	// Determine violation reason.
	if expired && overLimit {
		e.violation = "license expired and thrall count exceeds limit"
	} else if expired {
		e.violation = "license expired"
	} else {
		e.violation = fmt.Sprintf("active thralls exceed license limit (%d)", e.license.Payload.MaxThralls)
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
		return fmt.Errorf("license enforcement stage 3: %s — server must shut down (day %d of %d)", e.violation, elapsed, totalDaysToStop)
	case elapsed >= graceDays:
		e.stage = StageThrallBlock
	default:
		e.stage = StageGrace
	}

	return nil
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
	case StageThrallBlock, StageServiceStop:
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
			"grace_days_remaining", totalDaysToStop-elapsed)
	case StageThrallBlock:
		slog.Error("license violation: new thrall registrations blocked",
			"violation", e.violation,
			"license_id", e.license.Payload.LicenseID,
			"day", elapsed,
			"days_until_service_stop", totalDaysToStop-elapsed)
	}
}
