package watcher

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hobnob/hobnob/server/db"
)

// DBAdapter wraps *db.Queries to satisfy the watcher.DB interface,
// bridging type mismatches between the sqlc-generated layer and the
// watcher's simpler interface.
type DBAdapter struct {
	q *db.Queries
}

// NewDBAdapter creates a DBAdapter around the sqlc query layer.
func NewDBAdapter(q *db.Queries) *DBAdapter {
	return &DBAdapter{q: q}
}

func (a *DBAdapter) SetForcePush(ctx context.Context, id uuid.UUID) error {
	return a.q.SetForcePush(ctx, id)
}

func (a *DBAdapter) GetNodeByHostname(ctx context.Context, hostname string) (uuid.UUID, error) {
	node, err := a.q.GetNodeByHostname(ctx, hostname)
	if err != nil {
		return uuid.Nil, err
	}
	return node.ID, nil
}

func (a *DBAdapter) CreateSecretRotationEvent(ctx context.Context, params RotationRecord) error {
	_, err := a.q.CreateSecretRotationEvent(ctx, db.CreateSecretRotationEventParams{
		SecretPath:    params.SecretPath,
		OldVersion:    pgtype.Text{String: params.OldVersion, Valid: true},
		NewVersion:    pgtype.Text{String: params.NewVersion, Valid: true},
		AffectedNodes: params.AffectedNodes,
	})
	return err
}
