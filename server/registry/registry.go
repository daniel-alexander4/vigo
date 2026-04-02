// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package registry implements the enrollment registry for agent registration,
// approved pattern management, and envoy revocation.
package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
)

// bcryptCost is the bcrypt hash cost for one-time enrollment tokens.
const bcryptCost = 10

// Sentinel errors returned by Registry methods.
var (
	ErrNoMatchingPattern = errors.New("no matching approved pattern found for hostname or IP")
	ErrEnvoyRevoked       = errors.New("envoy is revoked")
	ErrEnvoyNotFound      = errors.New("envoy not found")
	ErrHostnameRevoked   = errors.New("hostname belongs to a revoked envoy and cannot re-enroll")
	ErrHostnameInUse     = errors.New("an active envoy with this hostname already exists (use force to replace)")
)

// Registry manages envoy enrollment: approved pattern creation, agent
// registration with glob matching and bcrypt token validation, and
// envoy revocation.
type Registry struct {
	queries *db.Queries
}

// NewRegistry creates a Registry backed by the given sqlc Queries.
func NewRegistry(queries *db.Queries) *Registry {
	return &Registry{queries: queries}
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
// a envoy record, marks the pattern as used, and returns the envoy UUID string.
//
// If force is true and an active envoy with the same hostname exists, the old
// envoy is revoked first. If force is false, registration is rejected.
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

	revokedID, err := r.checkHostnameConflict(ctx, hostname, force)
	if err != nil {
		return "", err
	}
	_ = revokedID

	// Fetch all unused approved patterns.
	patterns, err := r.queries.ListUnusedApprovedPatterns(ctx)
	if err != nil {
		return "", fmt.Errorf("listing unused patterns: %w", err)
	}

	if len(patterns) == 0 {
		return "", ErrNoMatchingPattern
	}

	// Filter by glob match on hostname OR IP, then try bcrypt comparison.
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

		// Glob matched -- now verify token via bcrypt.
		if err := bcrypt.CompareHashAndPassword([]byte(p.TokenHash), []byte(token)); err != nil {
			// Token doesn't match this pattern; continue checking others.
			continue
		}

		// Token matched. Create envoy.
		envoy, err := r.queries.CreateEnvoy(ctx, db.CreateEnvoyParams{
			Hostname:  hostname,
			IpAddress: ip,
			PubkeyPem: pubkeyPEM,
		})
		if err != nil {
			return "", fmt.Errorf("creating envoy: %w", err)
		}

		// Mark pattern as used.
		err = r.queries.MarkPatternUsed(ctx, db.MarkPatternUsedParams{
			ID: p.ID,
			UsedBy: sql.NullString{
				String: envoy.ID,
				Valid:  true,
			},
		})
		if err != nil {
			return "", fmt.Errorf("marking pattern used: %w", err)
		}

		return envoy.ID, nil
	}

	return "", ErrNoMatchingPattern
}

// TrustedRegister enrolls a envoy without a token by validating the hostname
// and source IP against trusted enrollment patterns. If the envoy's hostname
// matches a trusted pattern and its IP falls within an allowed CIDR, it is
// enrolled without a one-time token.
func (r *Registry) TrustedRegister(ctx context.Context, hostname, ip, pubkeyPEM string, trustedPatterns []config.TrustedEnrollment, peerIP string, force bool) (string, error) {
	if hostname == "" {
		return "", fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return "", fmt.Errorf("public key must not be empty")
	}

	revokedID, err := r.checkHostnameConflict(ctx, hostname, force)
	if err != nil {
		return "", err
	}
	_ = revokedID

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

		// Check CIDR restrictions if specified.
		if len(tp.CIDRs) > 0 && !matchesCIDR(peerIP, tp.CIDRs) {
			continue
		}

		// Trusted match. Create envoy.
		envoy, err := r.queries.CreateEnvoy(ctx, db.CreateEnvoyParams{
			Hostname:  hostname,
			IpAddress: ip,
			PubkeyPem: pubkeyPEM,
		})
		if err != nil {
			return "", fmt.Errorf("creating envoy: %w", err)
		}

		return envoy.ID, nil
	}

	return "", ErrNoMatchingPattern
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

// RevokeEnvoy marks a envoy as revoked, blocking all future check-ins.
func (r *Registry) RevokeEnvoy(ctx context.Context, envoyID string) error {
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

	return nil
}

// CreateEnvoy creates a envoy record directly without token validation.
// Used by EnrollEnvoy (hub→spoke enrollment routing) where the hub has
// already validated the enrollment token.
func (r *Registry) CreateEnvoy(ctx context.Context, hostname, ip, pubkeyPEM string, force bool) (string, error) {
	if hostname == "" {
		return "", fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return "", fmt.Errorf("public key must not be empty")
	}

	if _, err := r.checkHostnameConflict(ctx, hostname, force); err != nil {
		return "", err
	}

	envoy, err := r.queries.CreateEnvoy(ctx, db.CreateEnvoyParams{
		Hostname:  hostname,
		IpAddress: ip,
		PubkeyPem: pubkeyPEM,
	})
	if err != nil {
		return "", fmt.Errorf("creating envoy: %w", err)
	}

	return envoy.ID, nil
}

// checkHostnameConflict checks if a hostname is already in use by an active or
// revoked envoy. If force is true and an active envoy exists, it is revoked
// first. Returns the revoked envoy's ID (empty if none was revoked).
func (r *Registry) checkHostnameConflict(ctx context.Context, hostname string, force bool) (string, error) {
	existing, err := r.queries.GetActiveEnvoyByHostname(ctx, hostname)
	if err == nil {
		// Active envoy exists with this hostname.
		if !force {
			return "", ErrHostnameInUse
		}
		// Force: revoke the old envoy first.
		if err := r.queries.RevokeEnvoy(ctx, existing.ID); err != nil {
			return "", fmt.Errorf("revoking existing envoy %s: %w", existing.ID, err)
		}
		return existing.ID, nil
	}

	// Check for revoked envoy (ENRL-07).
	existing2, err := r.queries.GetEnvoyByHostname(ctx, hostname)
	if err == nil && existing2.Revoked != 0 {
		if !force {
			return "", ErrHostnameRevoked
		}
		// Force overrides ENRL-07.
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
