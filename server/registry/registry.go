// Package registry implements the enrollment registry for agent registration,
// approved pattern management, and node revocation.
package registry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/bcrypt"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
)

// bcryptCost is the bcrypt hash cost for one-time enrollment tokens.
const bcryptCost = 10

// Sentinel errors returned by Registry methods.
var (
	ErrNoMatchingPattern = errors.New("no matching approved pattern found for hostname or IP")
	ErrNodeRevoked       = errors.New("node is revoked")
	ErrNodeNotFound      = errors.New("node not found")
	ErrHostnameRevoked   = errors.New("hostname belongs to a revoked node and cannot re-enroll")
)

// Registry manages node enrollment: approved pattern creation, agent
// registration with glob matching and bcrypt token validation, and
// node revocation.
type Registry struct {
	queries *db.Queries
}

// NewRegistry creates a Registry backed by the given sqlc Queries.
func NewRegistry(queries *db.Queries) *Registry {
	return &Registry{queries: queries}
}

// AddApprovedPattern creates an approved pattern with a bcrypt-hashed token.
// Returns the pattern's UUID.
func (r *Registry) AddApprovedPattern(ctx context.Context, pattern, token string) (uuid.UUID, error) {
	if pattern == "" {
		return uuid.Nil, fmt.Errorf("pattern must not be empty")
	}
	if token == "" {
		return uuid.Nil, fmt.Errorf("token must not be empty")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcryptCost)
	if err != nil {
		return uuid.Nil, fmt.Errorf("hashing token: %w", err)
	}

	ap, err := r.queries.CreateApprovedPattern(ctx, db.CreateApprovedPatternParams{
		Pattern:   pattern,
		TokenHash: string(hash),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("creating approved pattern: %w", err)
	}

	return ap.ID, nil
}

// Register enrolls a new agent. It validates the hostname/IP against unused
// approved patterns using glob matching, verifies the token via bcrypt, creates
// a node record, marks the pattern as used, and returns the node UUID.
//
// The flow is:
//  1. Validate inputs
//  2. Check ENRL-07: reject if hostname belongs to a revoked node
//  3. Fetch all unused approved patterns
//  4. Filter by glob match on hostname OR IP (avoid bcrypt on non-matching patterns)
//  5. For glob-matched patterns, compare token via bcrypt
//  6. On match: create node, mark pattern used, return node UUID
func (r *Registry) Register(ctx context.Context, hostname, ip, pubkeyPEM, token string) (uuid.UUID, error) {
	if hostname == "" {
		return uuid.Nil, fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return uuid.Nil, fmt.Errorf("public key must not be empty")
	}
	if token == "" {
		return uuid.Nil, fmt.Errorf("token must not be empty")
	}

	// ENRL-07: Check if hostname belongs to a revoked node.
	existing, err := r.queries.GetNodeByHostname(ctx, hostname)
	if err == nil && existing.Revoked {
		return uuid.Nil, ErrHostnameRevoked
	}

	// Fetch all unused approved patterns.
	patterns, err := r.queries.ListUnusedApprovedPatterns(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("listing unused patterns: %w", err)
	}

	if len(patterns) == 0 {
		return uuid.Nil, ErrNoMatchingPattern
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

		// Token matched. Create node.
		node, err := r.queries.CreateNode(ctx, db.CreateNodeParams{
			Hostname:  hostname,
			IpAddress: ip,
			PubkeyPem: pubkeyPEM,
		})
		if err != nil {
			return uuid.Nil, fmt.Errorf("creating node: %w", err)
		}

		// Mark pattern as used.
		err = r.queries.MarkPatternUsed(ctx, db.MarkPatternUsedParams{
			ID: p.ID,
			UsedBy: pgtype.UUID{
				Bytes: node.ID,
				Valid: true,
			},
		})
		if err != nil {
			return uuid.Nil, fmt.Errorf("marking pattern used: %w", err)
		}

		return node.ID, nil
	}

	return uuid.Nil, ErrNoMatchingPattern
}

// TrustedRegister enrolls a node without a token by validating the hostname
// and source IP against trusted enrollment patterns. This is the Puppet-style
// autosigning path: if the node's hostname matches a trusted pattern and its
// IP falls within an allowed CIDR, it is enrolled without a one-time token.
func (r *Registry) TrustedRegister(ctx context.Context, hostname, ip, pubkeyPEM string, trustedPatterns []config.TrustedEnrollment, peerIP string) (uuid.UUID, error) {
	if hostname == "" {
		return uuid.Nil, fmt.Errorf("hostname must not be empty")
	}
	if pubkeyPEM == "" {
		return uuid.Nil, fmt.Errorf("public key must not be empty")
	}

	// ENRL-07: Check if hostname belongs to a revoked node.
	existing, err := r.queries.GetNodeByHostname(ctx, hostname)
	if err == nil && existing.Revoked {
		return uuid.Nil, ErrHostnameRevoked
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

		// Trusted match. Create node.
		node, err := r.queries.CreateNode(ctx, db.CreateNodeParams{
			Hostname:  hostname,
			IpAddress: ip,
			PubkeyPem: pubkeyPEM,
		})
		if err != nil {
			return uuid.Nil, fmt.Errorf("creating node: %w", err)
		}

		return node.ID, nil
	}

	return uuid.Nil, ErrNoMatchingPattern
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

// RevokeNode marks a node as revoked, blocking all future check-ins.
func (r *Registry) RevokeNode(ctx context.Context, nodeID uuid.UUID) error {
	// Verify node exists first.
	_, err := r.queries.GetNodeByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNodeNotFound
		}
		return fmt.Errorf("looking up node: %w", err)
	}

	if err := r.queries.RevokeNode(ctx, nodeID); err != nil {
		return fmt.Errorf("revoking node: %w", err)
	}

	return nil
}

// ValidateNode checks if a node exists and is not revoked. Returns an error
// if the node is revoked or does not exist.
func (r *Registry) ValidateNode(ctx context.Context, nodeID uuid.UUID) error {
	node, err := r.queries.GetNodeByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNodeNotFound
		}
		return fmt.Errorf("looking up node: %w", err)
	}

	if node.Revoked {
		return ErrNodeRevoked
	}

	return nil
}
