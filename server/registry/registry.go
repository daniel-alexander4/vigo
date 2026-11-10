// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

// Package registry implements the enrollment registry for agent registration,
// approved pattern management, and envoy revocation.
//
// All three enrollment entry points — token-based Register (gRPC Register
// handler), TrustedRegister (bootstrap HTTP + trusted CIDR patterns), and
// CreateEnvoy (bolt-to-bolt AdminService.EnrollEnvoy) — funnel through the same
// internal chokepoint. That chokepoint holds the registry-wide mutex for the
// duration of the credential check, the hostname-conflict check, the DB
// insert, and the FleetIndex update, which makes the operation atomic across
// all three paths. Credentials are always validated BEFORE the conflict check,
// because a forced conflict revokes the existing envoy.
//
// The FleetIndex is injected via SetFleetIndex so registry can keep the
// in-memory index in sync with DB writes under the same mutex.
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

	"git.alexander4.io/vigo/server/ca"
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
	// ErrInvalidHostname wraps a ca.ValidateHostname rejection. The enrolled
	// hostname becomes the CN/DNS SAN of the envoy's fleet-CA cert, so a
	// malformed, wildcard, or IP-literal name is refused before anything else.
	ErrInvalidHostname = errors.New("invalid hostname")
)

// validateEnrollHostname is the shared entry gate for all three enrollment
// paths.
func validateEnrollHostname(hostname string) error {
	if err := ca.ValidateHostname(hostname); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidHostname, err)
	}
	return nil
}

// Registry manages envoy enrollment: approved pattern creation, agent
// registration with glob matching and bcrypt token validation, and
// envoy revocation. All enrollment operations are serialized by a single
// mutex (enrollMu) so the hostname check and the DB + FleetIndex
// mutations form one atomic unit.
type Registry struct {
	queries *db.Queries

	enrollMu sync.Mutex // serializes all enrollment operations

	// Optional collaborator, set via SetFleetIndex. Nil-safe: a Registry
	// with no fleet index still functions (useful for tests), it just skips
	// the in-memory index update.
	fleet *fleet.FleetIndex
}

// NewRegistry creates a Registry backed by the given sqlc Queries.
// Callers should wire the fleet index via SetFleetIndex before accepting
// enrollment traffic.
func NewRegistry(queries *db.Queries) *Registry {
	return &Registry{queries: queries}
}

// SetFleetIndex wires the in-memory fleet index, which the registry updates
// atomically alongside each DB write.
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
// envoy is revoked, under the same mutex — but only after the token has
// matched an approved pattern, never for an unauthenticated caller.
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

	if err := validateEnrollHostname(hostname); err != nil {
		return "", err
	}

	r.enrollMu.Lock()
	defer r.enrollMu.Unlock()

	// The hostname-conflict check (which with force=true REVOKES the
	// existing envoy) runs only after the token has matched a pattern below:
	// an unauthenticated caller must never be able to revoke anything.
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

		// Pattern + token matched — the caller is authenticated, so the
		// hostname conflict (incl. force-revoke) may now be resolved.
		if _, err := r.checkHostnameConflictLocked(ctx, hostname, force); err != nil {
			return "", err
		}

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

	if err := validateEnrollHostname(hostname); err != nil {
		return "", err
	}

	r.enrollMu.Lock()
	defer r.enrollMu.Unlock()

	// As in Register: the conflict check (force-revoke) runs only once the
	// caller has matched a trusted pattern + CIDR.
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

		if _, err := r.checkHostnameConflictLocked(ctx, hostname, force); err != nil {
			return "", err
		}
		return r.insertEnvoyLocked(ctx, hostname, ip, pubkeyPEM)
	}

	return "", ErrNoMatchingPattern
}

// CreateEnvoy creates an envoy record without token validation. Used by
// AdminService.EnrollEnvoy (spanner enrollment routing), where the bolt that
// received the enrollment has already validated the token before calling the
// owning bolt.
func (r *Registry) CreateEnvoy(ctx context.Context, hostname, ip, pubkeyPEM string, force bool) (string, error) {
	if hostname == "" {
		return "", fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return "", fmt.Errorf("public key must not be empty")
	}

	if err := validateEnrollHostname(hostname); err != nil {
		return "", err
	}

	r.enrollMu.Lock()
	defer r.enrollMu.Unlock()

	if _, err := r.checkHostnameConflictLocked(ctx, hostname, force); err != nil {
		return "", err
	}

	return r.insertEnvoyLocked(ctx, hostname, ip, pubkeyPEM)
}

// SetEnvoyCertPin records an envoy's enrolled mTLS client-cert SPKI hash (hex
// sha256) so the check-in verify path can pin against it (cert-pinning). The
// bootstrap handler calls this once the CSR is signed, for the just-enrolled
// envoy. Re-enrollment mints a fresh cert and a fresh row, so this is the only
// update path; no separate rotation hook is needed.
func (r *Registry) SetEnvoyCertPin(ctx context.Context, envoyID, spki string) error {
	if err := r.queries.UpdateEnvoyCertPin(ctx, db.UpdateEnvoyCertPinParams{
		CertSpkiSha256: spki,
		ID:             envoyID,
	}); err != nil {
		return fmt.Errorf("storing cert pin for %s: %w", envoyID, err)
	}
	if r.fleet != nil {
		r.fleet.SetCertPin(envoyID, spki)
	}
	return nil
}

// insertEnvoyLocked is the single chokepoint where every enrollment path
// lands after its own validation. Caller must hold r.enrollMu. The DB insert
// and FleetIndex update happen under the same mutex.
func (r *Registry) insertEnvoyLocked(ctx context.Context, hostname, ip, pubkeyPEM string) (string, error) {
	envoy, err := r.queries.CreateEnvoy(ctx, db.CreateEnvoyParams{
		Hostname:  hostname,
		IpAddress: ip,
		PubkeyPem: pubkeyPEM,
	})
	if err != nil {
		return "", fmt.Errorf("creating envoy: %w", err)
	}

	// Mirror into FleetIndex under the same mutex as the DB insert.
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
// is revoked in both the DB and FleetIndex. Caller must hold r.enrollMu.
func (r *Registry) checkHostnameConflictLocked(ctx context.Context, hostname string, force bool) (string, error) {
	existing, err := r.queries.GetActiveEnvoyByHostname(ctx, hostname)
	if err == nil {
		// Active envoy exists with this hostname.
		if !force {
			return "", ErrHostnameInUse
		}
		// Force: revoke the old envoy first, both DB and FleetIndex.
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
