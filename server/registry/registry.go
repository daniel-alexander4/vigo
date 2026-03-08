// Package registry implements the enrollment registry for agent registration,
// approved pattern management, and client revocation.
package registry

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/bcrypt"

	"github.com/hobnob/hobnob/server/db"
)

// bcryptCost is the bcrypt hash cost for one-time enrollment tokens.
const bcryptCost = 10

// Sentinel errors returned by Registry methods.
var (
	ErrNoMatchingPattern = errors.New("no matching approved pattern found for hostname or IP")
	ErrClientRevoked     = errors.New("client is revoked")
	ErrClientNotFound    = errors.New("client not found")
	ErrHostnameRevoked   = errors.New("hostname belongs to a revoked client and cannot re-enroll")
)

// Registry manages client enrollment: approved pattern creation, agent
// registration with glob matching and bcrypt token validation, and
// client revocation.
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
// a client record, marks the pattern as used, and returns the client UUID.
//
// The flow is:
//  1. Validate inputs
//  2. Check ENRL-07: reject if hostname belongs to a revoked client
//  3. Fetch all unused approved patterns
//  4. Filter by glob match on hostname OR IP (avoid bcrypt on non-matching patterns)
//  5. For glob-matched patterns, compare token via bcrypt
//  6. On match: create client, mark pattern used, return client UUID
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

	// ENRL-07: Check if hostname belongs to a revoked client.
	existing, err := r.queries.GetClientByHostname(ctx, hostname)
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

		// Token matched. Create client.
		client, err := r.queries.CreateClient(ctx, db.CreateClientParams{
			Hostname:  hostname,
			IpAddress: ip,
			PubkeyPem: pubkeyPEM,
		})
		if err != nil {
			return uuid.Nil, fmt.Errorf("creating client: %w", err)
		}

		// Mark pattern as used.
		err = r.queries.MarkPatternUsed(ctx, db.MarkPatternUsedParams{
			ID: p.ID,
			UsedBy: pgtype.UUID{
				Bytes: client.ID,
				Valid: true,
			},
		})
		if err != nil {
			return uuid.Nil, fmt.Errorf("marking pattern used: %w", err)
		}

		return client.ID, nil
	}

	return uuid.Nil, ErrNoMatchingPattern
}

// RevokeClient marks a client as revoked, blocking all future check-ins.
func (r *Registry) RevokeClient(ctx context.Context, clientID uuid.UUID) error {
	// Verify client exists first.
	_, err := r.queries.GetClientByID(ctx, clientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrClientNotFound
		}
		return fmt.Errorf("looking up client: %w", err)
	}

	if err := r.queries.RevokeClient(ctx, clientID); err != nil {
		return fmt.Errorf("revoking client: %w", err)
	}

	return nil
}

// ValidateClient checks if a client exists and is not revoked. Returns an error
// if the client is revoked or does not exist.
func (r *Registry) ValidateClient(ctx context.Context, clientID uuid.UUID) error {
	client, err := r.queries.GetClientByID(ctx, clientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrClientNotFound
		}
		return fmt.Errorf("looking up client: %w", err)
	}

	if client.Revoked {
		return ErrClientRevoked
	}

	return nil
}
