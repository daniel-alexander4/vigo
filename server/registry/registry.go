// Package registry implements the enrollment registry for agent registration,
// approved pattern management, and client revocation.
package registry

import (
	"context"

	"github.com/google/uuid"
	"github.com/isogov/isogov/server/db"
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
	return uuid.Nil, nil
}

// Register enrolls a new agent. It validates the hostname/IP against unused
// approved patterns using glob matching, verifies the token via bcrypt, creates
// a client record, marks the pattern as used, and returns the client UUID.
func (r *Registry) Register(ctx context.Context, hostname, ip, pubkeyPEM, token string) (uuid.UUID, error) {
	return uuid.Nil, nil
}

// RevokeClient marks a client as revoked, blocking all future check-ins.
func (r *Registry) RevokeClient(ctx context.Context, clientID uuid.UUID) error {
	return nil
}

// ValidateClient checks if a client exists and is not revoked. Returns an error
// if the client is revoked or does not exist.
func (r *Registry) ValidateClient(ctx context.Context, clientID uuid.UUID) error {
	return nil
}
