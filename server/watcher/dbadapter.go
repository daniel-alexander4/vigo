package watcher

import (
	"context"
	"database/sql"

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

func (a *DBAdapter) SetForcePush(ctx context.Context, id string) error {
	return a.q.SetForcePush(ctx, id)
}

func (a *DBAdapter) GetThrallByHostname(ctx context.Context, hostname string) (string, error) {
	thrall, err := a.q.GetThrallByHostname(ctx, hostname)
	if err != nil {
		return "", err
	}
	return thrall.ID, nil
}

func (a *DBAdapter) CreateSecretRotationEvent(ctx context.Context, params RotationRecord) error {
	_, err := a.q.CreateSecretRotationEvent(ctx, db.CreateSecretRotationEventParams{
		SecretPath:    params.SecretPath,
		OldVersion:    sql.NullString{String: params.OldVersion, Valid: true},
		NewVersion:    sql.NullString{String: params.NewVersion, Valid: true},
		AffectedThralls: int64(params.AffectedThralls),
	})
	return err
}
