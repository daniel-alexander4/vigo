// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package watcher

import (
	"context"
	"database/sql"

	"git.alexander4.io/vigo/server/db"
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

func (a *DBAdapter) GetEnvoyByHostname(ctx context.Context, hostname string) (string, error) {
	envoy, err := a.q.GetEnvoyByHostname(ctx, hostname)
	if err != nil {
		return "", err
	}
	return envoy.ID, nil
}

func (a *DBAdapter) AppendRotatedSecret(ctx context.Context, id, secretPath string) error {
	return a.q.AppendRotatedSecret(ctx, id, secretPath)
}

func (a *DBAdapter) CreateSecretRotationEvent(ctx context.Context, params RotationRecord) error {
	_, err := a.q.CreateSecretRotationEvent(ctx, db.CreateSecretRotationEventParams{
		SecretPath:    params.SecretPath,
		OldVersion:    sql.NullString{String: params.OldVersion, Valid: true},
		NewVersion:    sql.NullString{String: params.NewVersion, Valid: true},
		AffectedEnvoys: int64(params.AffectedEnvoys),
	})
	return err
}
