// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

// Package registry implements the enrollment registry for agent registration,
// approved pattern management, and thrall revocation.
package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
)

// bcryptCost is the bcrypt hash cost for one-time enrollment tokens.
const bcryptCost = 10

// Sentinel errors returned by Registry methods.
var (
	ErrNoMatchingPattern = errors.New("no matching approved pattern found for hostname or IP")
	ErrThrallRevoked       = errors.New("thrall is revoked")
	ErrThrallNotFound      = errors.New("thrall not found")
	ErrHostnameRevoked   = errors.New("hostname belongs to a revoked thrall and cannot re-enroll")
)

// Registry manages thrall enrollment: approved pattern creation, agent
// registration with glob matching and bcrypt token validation, and
// thrall revocation.
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
// a thrall record, marks the pattern as used, and returns the thrall UUID string.
//
// The flow is:
//  1. Validate inputs
//  2. Check ENRL-07: reject if hostname belongs to a revoked thrall
//  3. Fetch all unused approved patterns
//  4. Filter by glob match on hostname OR IP (avoid bcrypt on non-matching patterns)
//  5. For glob-matched patterns, compare token via bcrypt
//  6. On match: create thrall, mark pattern used, return thrall UUID
func (r *Registry) Register(ctx context.Context, hostname, ip, pubkeyPEM, token string) (string, error) {
	if hostname == "" {
		return "", fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return "", fmt.Errorf("public key must not be empty")
	}
	if token == "" {
		return "", fmt.Errorf("token must not be empty")
	}

	// ENRL-07: Check if hostname belongs to a revoked thrall.
	existing, err := r.queries.GetThrallByHostname(ctx, hostname)
	if err == nil && existing.Revoked != 0 {
		return "", ErrHostnameRevoked
	}

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
		hostnameMatch, _ := filepath.Match(p.Pattern, hostname)
		ipMatch, _ := filepath.Match(p.Pattern, ip)

		if !hostnameMatch && !ipMatch {
			continue
		}

		// Glob matched -- now verify token via bcrypt.
		if err := bcrypt.CompareHashAndPassword([]byte(p.TokenHash), []byte(token)); err != nil {
			// Token doesn't match this pattern; continue checking others.
			continue
		}

		// Token matched. Create thrall.
		thrall, err := r.queries.CreateThrall(ctx, db.CreateThrallParams{
			Hostname:  hostname,
			IpAddress: ip,
			PubkeyPem: pubkeyPEM,
		})
		if err != nil {
			return "", fmt.Errorf("creating thrall: %w", err)
		}

		// Mark pattern as used.
		err = r.queries.MarkPatternUsed(ctx, db.MarkPatternUsedParams{
			ID: p.ID,
			UsedBy: sql.NullString{
				String: thrall.ID,
				Valid:  true,
			},
		})
		if err != nil {
			return "", fmt.Errorf("marking pattern used: %w", err)
		}

		return thrall.ID, nil
	}

	return "", ErrNoMatchingPattern
}

// TrustedRegister enrolls a thrall without a token by validating the hostname
// and source IP against trusted enrollment patterns. This is the Puppet-style
// autosigning path: if the thrall's hostname matches a trusted pattern and its
// IP falls within an allowed CIDR, it is enrolled without a one-time token.
func (r *Registry) TrustedRegister(ctx context.Context, hostname, ip, pubkeyPEM string, trustedPatterns []config.TrustedEnrollment, peerIP string) (string, error) {
	if hostname == "" {
		return "", fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return "", fmt.Errorf("public key must not be empty")
	}

	// ENRL-07: Check if hostname belongs to a revoked thrall.
	existing, err := r.queries.GetThrallByHostname(ctx, hostname)
	if err == nil && existing.Revoked != 0 {
		return "", ErrHostnameRevoked
	}

	for _, tp := range trustedPatterns {
		hostnameMatch, _ := filepath.Match(tp.Pattern, hostname)
		ipMatch, _ := filepath.Match(tp.Pattern, ip)

		if !hostnameMatch && !ipMatch {
			continue
		}

		// Check CIDR restrictions if specified.
		if len(tp.CIDRs) > 0 && !matchesCIDR(peerIP, tp.CIDRs) {
			continue
		}

		// Trusted match. Create thrall.
		thrall, err := r.queries.CreateThrall(ctx, db.CreateThrallParams{
			Hostname:  hostname,
			IpAddress: ip,
			PubkeyPem: pubkeyPEM,
		})
		if err != nil {
			return "", fmt.Errorf("creating thrall: %w", err)
		}

		return thrall.ID, nil
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

// RevokeThrall marks a thrall as revoked, blocking all future check-ins.
func (r *Registry) RevokeThrall(ctx context.Context, thrallID string) error {
	// Verify thrall exists first.
	_, err := r.queries.GetThrallByID(ctx, thrallID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrThrallNotFound
		}
		return fmt.Errorf("looking up thrall: %w", err)
	}

	if err := r.queries.RevokeThrall(ctx, thrallID); err != nil {
		return fmt.Errorf("revoking thrall: %w", err)
	}

	return nil
}

// ValidateThrall checks if a thrall exists and is not revoked. Returns an error
// if the thrall is revoked or does not exist.
// CreateThrall creates a thrall record directly without token validation.
// Used by EnrollThrall (hub→spoke enrollment routing) where the hub has
// already validated the enrollment token.
func (r *Registry) CreateThrall(ctx context.Context, hostname, ip, pubkeyPEM string) (string, error) {
	if hostname == "" {
		return "", fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return "", fmt.Errorf("public key must not be empty")
	}

	thrall, err := r.queries.CreateThrall(ctx, db.CreateThrallParams{
		Hostname:  hostname,
		IpAddress: ip,
		PubkeyPem: pubkeyPEM,
	})
	if err != nil {
		return "", fmt.Errorf("creating thrall: %w", err)
	}

	return thrall.ID, nil
}

func (r *Registry) ValidateThrall(ctx context.Context, thrallID string) error {
	thrall, err := r.queries.GetThrallByID(ctx, thrallID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrThrallNotFound
		}
		return fmt.Errorf("looking up thrall: %w", err)
	}

	if thrall.Revoked != 0 {
		return ErrThrallRevoked
	}

	return nil
}
