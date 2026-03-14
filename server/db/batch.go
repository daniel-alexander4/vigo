// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SetForcePushAllActive sets force_push=1 on all non-revoked thralls in a
// single UPDATE, replacing the N+1 loop in ForcePush("all").
func (q *Queries) SetForcePushAllActive(ctx context.Context) (int64, error) {
	result, err := q.db.ExecContext(ctx,
		"UPDATE thralls SET force_push = 1 WHERE revoked = 0")
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ThrallWithLatestRun pairs a thrall with its most recent run's status and
// changed count. Used by GetComplianceSummary to eliminate the N+1 query.
type ThrallWithLatestRun struct {
	Thrall
	LatestRunStatus       sql.NullString
	LatestRunChangedCount sql.NullInt64
}

// ListThrallsWithLatestRun returns all non-revoked thralls joined with their
// latest run in a single query using a window function, replacing the O(N^2)
// correlated subqueries that degraded at scale.
func (q *Queries) ListThrallsWithLatestRun(ctx context.Context) ([]ThrallWithLatestRun, error) {
	const query = `
SELECT n.id, n.hostname, n.ip_address, n.pubkey_pem, n.enrolled_at,
       n.last_seen, n.revoked, n.revoked_at, n.force_push, n.pending_rotated_secrets,
       n.tags, n.maintenance_until,
       lr.status, lr.changed_count
FROM thralls n
LEFT JOIN (
  SELECT thrall_id, status, changed_count,
         ROW_NUMBER() OVER (PARTITION BY thrall_id ORDER BY started_at DESC) AS rn
  FROM runs
) lr ON lr.thrall_id = n.id AND lr.rn = 1
WHERE n.revoked = 0
ORDER BY n.enrolled_at DESC`

	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ThrallWithLatestRun
	for rows.Next() {
		var i ThrallWithLatestRun
		if err := rows.Scan(
			&i.ID, &i.Hostname, &i.IpAddress, &i.PubkeyPem,
			&i.EnrolledAt, &i.LastSeen, &i.Revoked, &i.RevokedAt, &i.ForcePush,
			&i.PendingRotatedSecrets, &i.Tags, &i.MaintenanceUntil,
			&i.LatestRunStatus, &i.LatestRunChangedCount,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// BatchUpdateLastSeen updates last_seen=datetime('now') for multiple thrall IDs
// in a single UPDATE ... WHERE id IN (...), replacing per-thrall loops.
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
	query := fmt.Sprintf("UPDATE thralls SET last_seen = datetime('now') WHERE id IN (%s)",
		strings.Join(placeholders, ","))
	_, err := q.db.ExecContext(ctx, query, args...)
	return err
}
