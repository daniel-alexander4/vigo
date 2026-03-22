// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SetForcePushAllActive sets force_push=1 on all non-revoked envoys in a
// single UPDATE, replacing the N+1 loop in ForcePush("all").
func (q *Queries) SetForcePushAllActive(ctx context.Context) (int64, error) {
	result, err := q.db.ExecContext(ctx,
		"UPDATE envoys SET force_push = 1 WHERE revoked = 0")
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// EnvoyWithLatestRun pairs a envoy with its most recent run's status and
// changed count. Used by GetComplianceSummary to eliminate the N+1 query.
type EnvoyWithLatestRun struct {
	Envoy
	LatestRunID           sql.NullString
	LatestRunStatus       sql.NullString
	LatestRunChangedCount sql.NullInt64
}

// ListEnvoysWithLatestRun returns all non-revoked envoys joined with their
// latest run in a single query using a window function, replacing the O(N^2)
// correlated subqueries that degraded at scale.
func (q *Queries) ListEnvoysWithLatestRun(ctx context.Context) ([]EnvoyWithLatestRun, error) {
	const query = `
SELECT n.id, n.hostname, n.ip_address, n.pubkey_pem, n.enrolled_at,
       n.last_seen, n.revoked, n.revoked_at, n.force_push, n.pending_rotated_secrets,
       n.tags, n.maintenance_until,
       lr.id, lr.status, lr.changed_count
FROM envoys n
LEFT JOIN (
  SELECT id, envoy_id, status, changed_count,
         ROW_NUMBER() OVER (PARTITION BY envoy_id ORDER BY started_at DESC) AS rn
  FROM runs
) lr ON lr.envoy_id = n.id AND lr.rn = 1
WHERE n.revoked = 0
ORDER BY n.enrolled_at DESC`

	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []EnvoyWithLatestRun
	for rows.Next() {
		var i EnvoyWithLatestRun
		if err := rows.Scan(
			&i.ID, &i.Hostname, &i.IpAddress, &i.PubkeyPem,
			&i.EnrolledAt, &i.LastSeen, &i.Revoked, &i.RevokedAt, &i.ForcePush,
			&i.PendingRotatedSecrets, &i.Tags, &i.MaintenanceUntil,
			&i.LatestRunID, &i.LatestRunStatus, &i.LatestRunChangedCount,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// BatchUpdateLastSeen updates last_seen=datetime('now') for multiple envoy IDs
// in a single UPDATE ... WHERE id IN (...), replacing per-envoy loops.
func (q *Queries) BatchUpdateLastSeen(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("UPDATE envoys SET last_seen = datetime('now') WHERE id IN (%s)",
		strings.Join(placeholders, ","))
	_, err := q.db.ExecContext(ctx, query, args...)
	return err
}
