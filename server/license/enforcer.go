// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package license

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	vigolic "git.alexander4.io/tessera/license/products/vigo"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/metrics"
)

// Sentinel errors returned by the enforcement gate. Callers can errors.Is
// against these to distinguish "node limit reached" (operator action required)
// from "internal enforcement error" (investigate the server) from stage-based
// blocks (downgrade/expiry/fingerprint grace exhausted).
var (
	// ErrNodeLimitReached is returned when active envoys >= MaxEnvoys.
	// This is the hard count gate — no grace period, no retry.
	ErrNodeLimitReached = errors.New("license node limit reached")

	// ErrEnrollmentBlocked is returned when the staged enforcement has
	// progressed past the grace window (Stage 2 — EnrollBlock). Reserved
	// for license downgrade, expiry, or fingerprint mismatch scenarios.
	ErrEnrollmentBlocked = errors.New("license enrollment blocked by staged enforcement")

	// ErrServiceStopping is returned when the staged enforcement has
	// reached Stage 3 (ServiceStop) and the server is shutting down.
	ErrServiceStopping = errors.New("license service stop — server shutting down")

	// ErrCountUnavailable is returned when the enforcement gate cannot
	// determine the current active envoy count (e.g., FleetIndex not wired
	// up). Gate fails closed, which surfaces as PermissionDenied to the
	// agent.
	ErrCountUnavailable = errors.New("license enforcement: active envoy count unavailable")
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
	License        *vigolic.Payload `json:"license,omitempty"`
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
	license      *vigolic.Payload
	licenseDir   string
	graceDir     string
	caFile       string // TLS CA cert path for fingerprint computation
	counter      EnvoyCounter
	shutdownFn   ShutdownFunc
	metrics      *metrics.Metrics
	stage        Stage
	graceStart   *time.Time
	violation    string
	lastLogTime  time.Time
}

// NewEnforcer creates an enforcer that scans licenseDir for valid .lic files.
// It validates the license immediately and returns an error if no valid
// license is found or the license is at Stage 3 (service stop).
// caFile is the path to the TLS CA cert (used for machine fingerprint validation).
// m owns the license gauges/counters this enforcer mutates.
func NewEnforcer(licenseDir, graceDir, caFile string, counter EnvoyCounter, shutdownFn ShutdownFunc, m *metrics.Metrics) (*Enforcer, error) {
	e := &Enforcer{
		licenseDir: licenseDir,
		graceDir:   graceDir,
		caFile:     caFile,
		counter:    counter,
		shutdownFn: shutdownFn,
		metrics:    m,
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

// CurrentCount returns the live active-envoy count from the mode-aware
// counter the enforcer was constructed with: the local FleetIndex count in
// local mode, the gossip-aggregated fleet-wide count in aggregate mode. It is
// the licensed-count source of truth — the same value GetStatus, the UI, and
// the bootstrap response report. The enrollment gate calls this immediately
// before RegistrationAllowedAt (under the registry's enrollMu) so the count
// it checks reflects the license mode rather than always the local fleet.
//
// No enforcer lock is taken: counter is set once at construction and reads
// its own concurrency-safe sources (FleetIndex / spanner observability cache),
// independent of the mu-protected enforcement state.
func (e *Enforcer) CurrentCount(ctx context.Context) (int, error) {
	return e.counter(ctx)
}

// RegistrationAllowedAt returns nil if a new envoy registration is permitted
// given the supplied live active envoy count, or a sentinel error wrapped with
// context if it is blocked. The count is passed in (not read inside this
// method) so the caller can ensure the count-check and the subsequent DB
// insert happen under the same mutex — without that ordering, two concurrent
// registrations at the limit could both pass the gate.
//
// Enforcement logic, in order:
//  1. Staged enforcement block — if the enforcer has progressed past the
//     downgrade/expiry/fingerprint grace window (Stage 2 EnrollBlock or
//     Stage 3 ServiceStop), reject immediately.
//  2. Hard node-count gate — if the current active count is at or above
//     MaxEnvoys, reject immediately with no grace period. This is the
//     primary enforcement mechanism for normal enrollment overflow.
//
// The hard gate and the staged enforcement are independent. Normal
// enrollments cannot trip the staged enforcement because this gate blocks
// them first; the staged enforcement only fires on license downgrade,
// expiry, or fingerprint mismatch — scenarios where the count exceeds the
// limit through license-side changes rather than through enrollment.
func (e *Enforcer) RegistrationAllowedAt(currentCount int) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Staged enforcement block (downgrade / expiry / fingerprint).
	switch e.stage {
	case StageEnrollBlock:
		return fmt.Errorf("%w: enforcement stage 2 — %s", ErrEnrollmentBlocked, e.violation)
	case StageServiceStop:
		return fmt.Errorf("%w: enforcement stage 3 — %s", ErrServiceStopping, e.violation)
	}

	// Hard node-count gate. count >= max means the next insert would push
	// the fleet over the limit. Rejects with no grace period.
	if e.license != nil && e.license.MaxEnvoys > 0 {
		if currentCount >= e.license.MaxEnvoys {
			return fmt.Errorf("%w: %d/%d active envoys — upgrade your license or decommission nodes before enrolling more",
				ErrNodeLimitReached, currentCount, e.license.MaxEnvoys)
		}
	}
	return nil
}

// RecordEnforcementError increments the Prometheus counter that tracks
// internal errors inside the enforcement gate (e.g., count unavailable,
// FleetIndex not wired up). Callers should invoke this when failing closed
// on a non-policy error so operators can distinguish genuine limit hits
// from server-side bugs.
func (e *Enforcer) RecordEnforcementError() {
	e.metrics.LicenseEnforcementErrors.Inc()
}

// RecordEnrollmentBlocked increments the Prometheus counter tracking
// enrollments rejected by the hard node-count gate specifically.
func (e *Enforcer) RecordEnrollmentBlocked() {
	e.metrics.LicenseEnrollmentsBlocked.Inc()
}

// GetStatus returns the current enforcement status for the API.
func (e *Enforcer) GetStatus() Status {
	e.mu.RLock()
	defer e.mu.RUnlock()

	s := Status{
		Stage: e.stage,
	}

	if e.license != nil {
		s.License = e.license
		s.MaxEnvoys = e.license.MaxEnvoys
		s.ExpiresAt = e.license.ExpiresAt

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
			shutdown := false
			daemon.Guard("license-enforcer", func() { shutdown = e.checkTick() })
			if shutdown {
				e.shutdownFn()
				return
			}
		}
	}
}

// checkTick runs one enforcement evaluation under the enforcer lock and reports
// whether stage 3 (service stop) has been reached. The lock is released via
// defer, so a panic in loadAndCheckLocked/logIfNeeded unwinds it cleanly —
// letting daemon.Guard recover and the next tick re-acquire the lock instead of
// deadlocking on a lock the panicked goroutine left held. The shutdown callback
// is invoked by the caller, outside the lock.
func (e *Enforcer) checkTick() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.loadAndCheckLocked()
	e.logIfNeeded()
	if e.stage == StageServiceStop && e.shutdownFn != nil {
		slog.Error("license enforcement: stage 3 reached — initiating server shutdown",
			"license_id", e.license.LicenseID,
			"violation", e.violation)
		return true
	}
	return false
}

// loadAndCheck loads the license and evaluates enforcement. Acquires lock.
func (e *Enforcer) loadAndCheck() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loadAndCheckLocked()
}

// loadAndCheckLocked does the work without acquiring the lock.
func (e *Enforcer) loadAndCheckLocked() error {
	_, payload, path, err := vigolic.LoadDir(e.licenseDir)
	if err != nil {
		return fmt.Errorf("%w\nPlace a .lic file in %s or install with: vigocli license install --file=<path>", err, e.licenseDir)
	}

	slog.Info("license loaded", "path", path)
	e.license = payload

	// Paid-tier licenses must be machine-bound. Reject installation of
	// any paid-tier license that wasn't issued with a fingerprint —
	// `RequiresBinding` is the single source of truth (tessera's
	// `license create` enforces the same rule at issuance, so a license
	// reaching this point without a fingerprint either predates the
	// rule or was signed out-of-band). Community (free) licenses fall
	// through to the existing optional-binding path below.
	if vigolic.RequiresBinding(payload.Tier) && payload.Fingerprint == "" {
		slog.Warn("license fingerprint missing — paid tier requires machine binding",
			"tier", payload.Tier,
		)
		e.violation = fmt.Sprintf("license fingerprint missing — %s tier requires machine binding. Regenerate the license with --fingerprint set; get the fingerprint via `vigocli license fingerprint`.", payload.Tier)
		e.stage = StageGrace
		if e.graceStart == nil {
			now := time.Now()
			e.graceStart = &now
			if err := writeGraceStart(e.graceDir, now); err != nil {
				slog.Warn("license grace start not persisted — grace clock resets on restart", "error", err)
			}
		}
		return nil
	}

	// Validate machine fingerprint for licenses that carry one.
	// Community (no fingerprint) and any other tier without binding fall
	// through; mismatch on a fingerprinted license triggers staged
	// enforcement.
	if payload.Fingerprint != "" {
		actual, fpErr := vigolic.MachineFingerprint(e.caFile)
		if fpErr != nil {
			slog.Warn("license fingerprint check: could not compute machine fingerprint", "error", fpErr)
		} else if actual != payload.Fingerprint {
			slog.Warn("license fingerprint mismatch — license is bound to a different machine",
				"expected", payload.Fingerprint[:12]+"...",
				"actual", actual[:12]+"...",
			)
			e.violation = "license fingerprint mismatch — this license is bound to a different machine. Contact support for a new license."
			e.stage = StageGrace
			if e.graceStart == nil {
				now := time.Now()
				e.graceStart = &now
				if err := writeGraceStart(e.graceDir, now); err != nil {
					slog.Warn("license grace start not persisted — grace clock resets on restart", "error", err)
				}
			}
			return nil
		}
	}

	return e.evaluateLocked()
}

// evaluateLocked determines the current enforcement stage. The hard
// node-count gate in RegistrationAllowedAt blocks normal enrollment
// overflow before it can ever reach this function, so the overLimit
// branch below is only reachable through one of three scenarios:
//   - License downgrade: operator installs a smaller license on a fleet
//     already above the new cap.
//   - License expiry: ExpiresAt has passed (overLimit is irrelevant here
//     but is still computed for the combined violation message).
//   - Post-restart state where the fleet was somehow over-limit before
//     this build was deployed (legacy/migration).
//
// In all three cases, the staged enforcement (Grace → EnrollBlock →
// ServiceStop) provides a runway to reconcile. Fingerprint mismatch is
// handled separately in loadAndCheckLocked.
func (e *Enforcer) evaluateLocked() error {
	now := time.Now()

	expired := false
	if exp, err := e.license.ExpiresTime(); err == nil {
		expired = now.After(exp)
	}

	overLimit := false
	if count, err := e.counter(context.Background()); err == nil {
		overLimit = count > e.license.MaxEnvoys
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
		e.violation = fmt.Sprintf("active envoys exceed license limit (%d)", e.license.MaxEnvoys)
	}

	// Load or create grace start timestamp.
	if e.graceStart == nil {
		if gs, err := readGraceStart(e.graceDir); err == nil {
			e.graceStart = &gs
		} else {
			t := now
			e.graceStart = &t
			if err := writeGraceStart(e.graceDir, t); err != nil {
				slog.Warn("license grace start not persisted — grace clock resets on restart", "error", err)
			}
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

// updateMetrics sets Prometheus gauges for current license state. It only
// reads enforcer fields and writes thread-safe gauges, so it must be called
// with e.mu held for read or write (callers under evaluateLocked hold the
// write lock; RefreshMetrics holds the read lock).
func (e *Enforcer) updateMetrics() {
	e.metrics.LicenseStage.Set(float64(e.stage))
	if e.license != nil {
		e.metrics.LicenseEnvoysMax.Set(float64(e.license.MaxEnvoys))
		if exp, err := e.license.ExpiresTime(); err == nil {
			days := int(time.Until(exp).Hours() / 24)
			e.metrics.LicenseDaysRemaining.Set(float64(days))
			e.metrics.LicenseExpiryTimestamp.Set(float64(exp.Unix()))
		}
	}
	if count, err := e.counter(context.Background()); err == nil {
		e.metrics.LicenseEnvoysUsed.Set(float64(count))
	}
}

// RefreshMetrics re-reads the live envoy count and re-sets the license gauges
// from the enforcer's mode-aware counter. It is cheap — a counter read plus a
// handful of gauge Sets, no license-file read or fingerprint recompute — so it
// is safe to call on the metrics-collector cadence (default 30s) rather than
// waiting for the hourly enforcement tick. Without this, vigo_license_envoys_used
// lags up to an hour behind a bulk envoy deletion (the gauge's only consumer is
// Prometheus; the UI already reads the live FleetIndex). updateMetrics only
// reads enforcer state and writes thread-safe prometheus gauges, so a read lock
// is sufficient.
func (e *Enforcer) RefreshMetrics() {
	e.mu.RLock()
	defer e.mu.RUnlock()
	e.updateMetrics()
}

// IsCommercialLicense reports whether the loaded license is a paid,
// commercial tier. Spanner federation requires one (server.go gates on
// it). The classification is tessera's: a tier is commercial iff it
// requires machine binding — i.e. it maps to a paid bracket
// (PricePerNode > 0). Community (free), the legacy aliases, and any
// unknown or empty tier are non-commercial. RequiresBinding is the
// single source of truth, so "commercial" can never drift from the
// tier/price table that defines it.
func (e *Enforcer) IsCommercialLicense() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.license != nil && vigolic.RequiresBinding(e.license.Tier)
}

// CheckInWarning returns a non-empty warning message if the license is in
// grace period, enrollment block, or if the fleet is at/near the node cap.
// The check-in handler logs this on every check-in so operators see it in
// their logs even when they are not watching the web UI.
func (e *Enforcer) CheckInWarning() string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.stage == StageServiceStop {
		return ""
	}

	// Capacity warnings (independent of stage) — compliant fleets that
	// are at or near the cap still need operator attention because the
	// hard gate will start rejecting enrollments at count == max.
	if e.stage == StageCompliant && e.license != nil && e.license.MaxEnvoys > 0 {
		if count, err := e.counter(context.Background()); err == nil {
			max := e.license.MaxEnvoys
			if count >= max {
				return fmt.Sprintf("license at capacity: %d/%d — new enrollments will be rejected", count, max)
			}
			if count*10 >= max*9 { // ≥ 90%
				return fmt.Sprintf("license nearing capacity: %d/%d — %d slot(s) remaining", count, max, max-count)
			}
		}
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

// HasFeature checks if the current license includes a named feature (e.g., "connwaer").
func (e *Enforcer) HasFeature(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.license == nil {
		return false
	}
	for _, f := range e.license.Features {
		if f == name {
			return true
		}
	}
	return false
}

// logIfNeeded emits log messages at the appropriate cadence per stage and
// per capacity level. Compliant fleets at ≥90% of the cap get an hourly
// WARN; at-capacity gets an hourly ERROR; both fire independently of the
// stage machine's grace/block timers.
func (e *Enforcer) logIfNeeded() {
	now := time.Now()
	var interval time.Duration

	// Capacity logging for compliant fleets: hourly when at or near cap.
	if e.stage == StageCompliant {
		if e.license == nil || e.license.MaxEnvoys == 0 {
			return
		}
		count, err := e.counter(context.Background())
		if err != nil {
			return
		}
		max := e.license.MaxEnvoys
		if count < (max*9)/10 { // below 90% — no log
			return
		}
		if now.Sub(e.lastLogTime) < time.Hour {
			return
		}
		e.lastLogTime = now
		if count >= max {
			slog.Error("license at capacity",
				"active_envoys", count,
				"max_envoys", max,
				"license_id", e.license.LicenseID)
		} else {
			slog.Warn("license nearing capacity",
				"active_envoys", count,
				"max_envoys", max,
				"slots_remaining", max-count,
				"license_id", e.license.LicenseID)
		}
		return
	}

	switch e.stage {
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
			"license_id", e.license.LicenseID,
			"day", elapsed,
			"grace_days_remaining", graceDays-elapsed)
	case StageEnrollBlock:
		slog.Error("license violation: new enrollments blocked",
			"violation", e.violation,
			"license_id", e.license.LicenseID,
			"day", elapsed,
			"days_until_service_stop", totalDaysToStop-elapsed)
	}
}
