// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package registry implements the enrollment registry for agent registration,
// approved pattern management, and envoy revocation.
//
// All three enrollment entry points — token-based Register (gRPC Register
// handler), TrustedRegister (bootstrap HTTP + trusted CIDR patterns), and
// CreateEnvoy (hub→bolt AdminService.EnrollEnvoy) — funnel through the same
// internal chokepoint. That chokepoint holds the registry-wide mutex for the
// duration of the license gate check, the DB insert, and the FleetIndex
// update, which makes the operation atomic across all three paths: two
// concurrent registrations at the node limit cannot both pass the gate.
//
// The license gate is injected via SetEnrollmentGate so this package does not
// import server/license directly. The FleetIndex is injected via
// SetFleetIndex so registry can keep the in-memory active count in sync with
// DB writes under the same mutex.
package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
)

// bcryptCost is the bcrypt hash cost for one-time enrollment tokens.
const bcryptCost = 10

// Sentinel errors returned by Registry methods.
var (
	ErrNoMatchingPattern = errors.New("no matching approved pattern found for hostname or IP")
	ErrEnvoyRevoked      = errors.New("envoy is revoked")
	ErrEnvoyNotFound     = errors.New("envoy not found")
	ErrHostnameRevoked   = errors.New("hostname belongs to a revoked envoy and cannot re-enroll")
	ErrHostnameInUse     = errors.New("an active envoy with this hostname already exists (use force to replace)")
)

// EnrollmentGate is the policy interface the registry consults before
// inserting a new envoy. Implemented by *license.Enforcer. Defined here as an
// interface to avoid a registry → license import cycle and to make the
// registry unit-testable with a trivial stub.
type EnrollmentGate interface {
	// CurrentCount returns the licensed-count source of truth: the local
	// fleet count in local mode, the gossip-aggregated fleet-wide count in
	// aggregate mode. The registry reads it here (rather than off its own
	// FleetIndex) so the gate enforces against the correct count for the
	// license mode, then passes the result to RegistrationAllowedAt under
	// the same mutex.
	CurrentCount(ctx context.Context) (int, error)

	// RegistrationAllowedAt returns nil if a new envoy may be registered
	// given the supplied live active count, or an error describing why it
	// is blocked. Implementations must not read the count themselves —
	// passing it in guarantees the caller's count-check and DB insert
	// happen under the same mutex.
	RegistrationAllowedAt(currentCount int) error

	// RecordEnforcementError increments an internal counter when the gate
	// fails closed on a non-policy error (e.g., count unavailable).
	RecordEnforcementError()

	// RecordEnrollmentBlocked increments an internal counter when the
	// hard node-count gate rejects an enrollment specifically.
	RecordEnrollmentBlocked()
}

// Registry manages envoy enrollment: approved pattern creation, agent
// registration with glob matching and bcrypt token validation, and
// envoy revocation. All enrollment operations are serialized by a single
// mutex (enrollMu) so the license gate check and the DB + FleetIndex
// mutations form one atomic unit.
type Registry struct {
	queries *db.Queries

	enrollMu sync.Mutex // serializes all enrollment operations

	// Optional collaborators, set via Set*. Nil-safe: a Registry with no
	// gate or fleet index still functions (useful for tests), it just
	// skips the license check and the in-memory index update.
	gate  EnrollmentGate
	fleet *fleet.FleetIndex
}

// NewRegistry creates a Registry backed by the given sqlc Queries.
// Callers should wire the license gate via SetEnrollmentGate and the
// fleet index via SetFleetIndex before accepting enrollment traffic.
func NewRegistry(queries *db.Queries) *Registry {
	return &Registry{queries: queries}
}

// SetEnrollmentGate installs the license policy gate. If nil, the registry
// runs without license enforcement — intended only for tests.
func (r *Registry) SetEnrollmentGate(g EnrollmentGate) {
	r.enrollMu.Lock()
	defer r.enrollMu.Unlock()
	r.gate = g
}

// SetFleetIndex wires the in-memory fleet index. The registry uses it both
// for the live active count (driving the license gate) and for inserting
// new envoys atomically alongside the DB write.
func (r *Registry) SetFleetIndex(fi *fleet.FleetIndex) {
	r.enrollMu.Lock()
	defer r.enrollMu.Unlock()
	r.fleet = fi
}

// AddApprovedPattern creates an approved pattern with a bcrypt-hashed token.
// Returns the pattern's UUID string.
func (r *Registry) AddApprovedPattern(ctx context.Context, pattern, token string) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("pattern must not be empty")
	}
	if token == "" {
		return "", fmt.Errorf("token must not be empty")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hashing token: %w", err)
	}

	ap, err := r.queries.CreateApprovedPattern(ctx, db.CreateApprovedPatternParams{
		Pattern:   pattern,
		TokenHash: string(hash),
	})
	if err != nil {
		return "", fmt.Errorf("creating approved pattern: %w", err)
	}

	return ap.ID, nil
}

// Register enrolls a new agent. It validates the hostname/IP against unused
// approved patterns using glob matching, verifies the token via bcrypt, creates
// an envoy record, marks the pattern as used, and returns the envoy UUID string.
//
// If force is true and an active envoy with the same hostname exists, the old
// envoy is revoked first — under the same mutex — so a force-replace at the
// node limit is net-zero and the license gate sees the post-revoke count.
// If force is false, registration is rejected when the hostname is in use.
func (r *Registry) Register(ctx context.Context, hostname, ip, pubkeyPEM, token string, force bool) (string, error) {
	if hostname == "" {
		return "", fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return "", fmt.Errorf("public key must not be empty")
	}
	if token == "" {
		return "", fmt.Errorf("token must not be empty")
	}

	r.enrollMu.Lock()
	defer r.enrollMu.Unlock()

	if _, err := r.checkHostnameConflictLocked(ctx, hostname, force); err != nil {
		return "", err
	}

	patterns, err := r.queries.ListUnusedApprovedPatterns(ctx)
	if err != nil {
		return "", fmt.Errorf("listing unused patterns: %w", err)
	}
	if len(patterns) == 0 {
		return "", ErrNoMatchingPattern
	}

	for _, p := range patterns {
		hostnameMatch, hErr := filepath.Match(p.Pattern, hostname)
		ipMatch, iErr := filepath.Match(p.Pattern, ip)
		if hErr != nil || iErr != nil {
			slog.Warn("invalid glob pattern in approved patterns", "pattern", p.Pattern, "error", hErr)
			continue
		}
		if !hostnameMatch && !ipMatch {
			continue
		}

		if err := bcrypt.CompareHashAndPassword([]byte(p.TokenHash), []byte(token)); err != nil {
			// Token doesn't match this pattern; continue checking others.
			continue
		}

		// Pattern + token matched — run the gate and create the envoy.
		envoyID, err := r.insertEnvoyLocked(ctx, hostname, ip, pubkeyPEM)
		if err != nil {
			return "", err
		}

		// Mark the one-time pattern consumed. If this fails the envoy is
		// already created but the pattern stays unused — leaving the
		// one-time token reusable (a Non-Negotiable: tokens are one-shot).
		// Compensate by deleting the just-created envoy (DB + FleetIndex)
		// so a failed enrollment leaves no trace and the token can't be
		// replayed. Still under enrollMu, so no concurrent enrollment can
		// observe the half-created state.
		if err := r.queries.MarkPatternUsed(ctx, db.MarkPatternUsedParams{
			ID:     p.ID,
			UsedBy: sql.NullString{String: envoyID, Valid: true},
		}); err != nil {
			if delErr := r.queries.DeleteEnvoy(ctx, envoyID); delErr != nil {
				slog.Error("registry: failed to roll back envoy after MarkPatternUsed failure — one-time token may be reusable",
					"envoy_id", envoyID, "error", delErr)
			}
			if r.fleet != nil {
				r.fleet.RemoveEnvoy(envoyID)
			}
			return "", fmt.Errorf("marking pattern used: %w", err)
		}

		return envoyID, nil
	}

	return "", ErrNoMatchingPattern
}

// TrustedRegister enrolls an envoy without a one-time token by validating the
// hostname and source IP against trusted enrollment patterns. Used by the
// bootstrap HTTP path when the operator has configured trusted CIDRs.
func (r *Registry) TrustedRegister(ctx context.Context, hostname, ip, pubkeyPEM string, trustedPatterns []config.TrustedEnrollment, peerIP string, force bool) (string, error) {
	if hostname == "" {
		return "", fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return "", fmt.Errorf("public key must not be empty")
	}

	r.enrollMu.Lock()
	defer r.enrollMu.Unlock()

	if _, err := r.checkHostnameConflictLocked(ctx, hostname, force); err != nil {
		return "", err
	}

	for _, tp := range trustedPatterns {
		hostnameMatch, hErr := filepath.Match(tp.Pattern, hostname)
		ipMatch, iErr := filepath.Match(tp.Pattern, ip)
		if hErr != nil || iErr != nil {
			slog.Warn("invalid glob pattern in trusted patterns", "pattern", tp.Pattern, "error", hErr)
			continue
		}
		if !hostnameMatch && !ipMatch {
			continue
		}
		if len(tp.CIDRs) > 0 && !matchesCIDR(peerIP, tp.CIDRs) {
			continue
		}

		return r.insertEnvoyLocked(ctx, hostname, ip, pubkeyPEM)
	}

	return "", ErrNoMatchingPattern
}

// CreateEnvoy creates an envoy record without token validation. Used by
// AdminService.EnrollEnvoy (hub→bolt enrollment routing) where the hub has
// already validated the enrollment token before calling into the bolt.
// Still runs the license gate so bolt-level limits are enforced.
func (r *Registry) CreateEnvoy(ctx context.Context, hostname, ip, pubkeyPEM string, force bool) (string, error) {
	if hostname == "" {
		return "", fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return "", fmt.Errorf("public key must not be empty")
	}

	r.enrollMu.Lock()
	defer r.enrollMu.Unlock()

	if _, err := r.checkHostnameConflictLocked(ctx, hostname, force); err != nil {
		return "", err
	}

	return r.insertEnvoyLocked(ctx, hostname, ip, pubkeyPEM)
}

// insertEnvoyLocked is the single chokepoint where every enrollment path
// lands after its own validation. Caller must hold r.enrollMu. The license
// gate, DB insert, and FleetIndex update happen in that order under the
// same mutex, so two concurrent enrollments at the node limit cannot both
// pass the gate.
func (r *Registry) insertEnvoyLocked(ctx context.Context, hostname, ip, pubkeyPEM string) (string, error) {
	// License gate — hard count check before any DB work.
	if r.gate != nil {
		if r.fleet == nil {
			r.gate.RecordEnforcementError()
			return "", fmt.Errorf("license enforcement: fleet index not wired up")
		}
		// Source the count from the gate's mode-aware counter, not the local
		// FleetIndex directly: in aggregate mode (spanner) the gate must
		// enforce against the gossip-wide fleet count, not just this bolt's
		// partition. In local mode this resolves to the same FleetIndex count.
		// Read under enrollMu so the check and the DB+index insert stay atomic.
		currentCount, err := r.gate.CurrentCount(ctx)
		if err != nil {
			r.gate.RecordEnforcementError()
			return "", fmt.Errorf("license enforcement: count unavailable: %w", err)
		}
		if err := r.gate.RegistrationAllowedAt(currentCount); err != nil {
			r.gate.RecordEnrollmentBlocked()
			return "", err
		}
	}

	envoy, err := r.queries.CreateEnvoy(ctx, db.CreateEnvoyParams{
		Hostname:  hostname,
		IpAddress: ip,
		PubkeyPem: pubkeyPEM,
	})
	if err != nil {
		return "", fmt.Errorf("creating envoy: %w", err)
	}

	// Mirror into FleetIndex under the same mutex so the next gate check
	// sees the incremented count. The callers formerly did this themselves
	// after Registry returned, which left a TOCTOU window where two
	// concurrent registrations could both observe a pre-insert count.
	if r.fleet != nil {
		pubKey, _ := db.ParsePubKey(pubkeyPEM)
		r.fleet.AddEnvoy(fleet.EnvoyState{
			ID:                envoy.ID,
			Hostname:          hostname,
			IP:                ip,
			PubkeyPEM:         pubkeyPEM,
			EnrolledAt:        time.Now(),
			ConvergenceStatus: "no data",
		}, pubKey)
	}

	return envoy.ID, nil
}

// matchesCIDR checks if an IP address falls within any of the given CIDRs.
func matchesCIDR(ipStr string, cidrs []string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// RevokeEnvoy marks an envoy as revoked, blocking all future check-ins.
// Decrements the fleet active count so subsequent enrollments see the
// newly-freed slot.
func (r *Registry) RevokeEnvoy(ctx context.Context, envoyID string) error {
	r.enrollMu.Lock()
	defer r.enrollMu.Unlock()

	// Verify envoy exists first.
	_, err := r.queries.GetEnvoyByID(ctx, envoyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEnvoyNotFound
		}
		return fmt.Errorf("looking up envoy: %w", err)
	}

	if err := r.queries.RevokeEnvoy(ctx, envoyID); err != nil {
		return fmt.Errorf("revoking envoy: %w", err)
	}

	if r.fleet != nil {
		r.fleet.SetRevoked(envoyID, true)
	}

	return nil
}

// checkHostnameConflictLocked checks if a hostname is already in use by an
// active or revoked envoy. If force is true and an active envoy exists, it
// is revoked in both the DB and FleetIndex (freeing a slot for the gate
// check that follows). Caller must hold r.enrollMu.
func (r *Registry) checkHostnameConflictLocked(ctx context.Context, hostname string, force bool) (string, error) {
	existing, err := r.queries.GetActiveEnvoyByHostname(ctx, hostname)
	if err == nil {
		// Active envoy exists with this hostname.
		if !force {
			return "", ErrHostnameInUse
		}
		// Force: revoke the old envoy first, both DB and FleetIndex, so
		// the subsequent gate check sees the decremented count. This
		// makes --force replacement net-zero at the node limit: the
		// slot is freed before the gate is consulted.
		if err := r.queries.RevokeEnvoy(ctx, existing.ID); err != nil {
			return "", fmt.Errorf("revoking existing envoy %s: %w", existing.ID, err)
		}
		if r.fleet != nil {
			r.fleet.SetRevoked(existing.ID, true)
		}
		return existing.ID, nil
	}

	// Check for revoked envoy with this hostname (ENRL-07).
	existing2, err := r.queries.GetEnvoyByHostname(ctx, hostname)
	if err == nil && existing2.Revoked != 0 {
		if !force {
			return "", ErrHostnameRevoked
		}
		// Force overrides ENRL-07. No counter decrement — already
		// revoked so the count is already accurate.
	}

	return "", nil
}

func (r *Registry) ValidateEnvoy(ctx context.Context, envoyID string) error {
	envoy, err := r.queries.GetEnvoyByID(ctx, envoyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEnvoyNotFound
		}
		return fmt.Errorf("looking up envoy: %w", err)
	}

	if envoy.Revoked != 0 {
		return ErrEnvoyRevoked
	}

	return nil
}
