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
// changed count. Used by GetConvergenceSummary to eliminate the N+1 query.
type EnvoyWithLatestRun struct {
	Envoy
	LatestRunID           sql.NullString
	LatestRunStatus       sql.NullString
	LatestRunChangedCount sql.NullInt64
	LatestRunFailedCount  sql.NullInt64
	LatestRunModuleCount  sql.NullInt64
}

// ListEnvoysWithLatestRun returns all non-revoked envoys joined with their
// latest run in a single query using a window function, replacing the O(N^2)
// correlated subqueries that degraded at scale.
func (q *Queries) ListEnvoysWithLatestRun(ctx context.Context) ([]EnvoyWithLatestRun, error) {
	const query = `
SELECT n.id, n.hostname, n.ip_address, n.pubkey_pem, n.enrolled_at,
       n.last_seen, n.revoked, n.revoked_at, n.force_push, n.pending_rotated_secrets,
       n.maintenance_until,
       lr.id, lr.status, lr.changed_count, lr.failed_count, lr.configcrate_count
FROM envoys n
LEFT JOIN (
  SELECT id, envoy_id, status, changed_count, failed_count, configcrate_count,
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
			&i.PendingRotatedSecrets, &i.MaintenanceUntil,
			&i.LatestRunID, &i.LatestRunStatus, &i.LatestRunChangedCount,
			&i.LatestRunFailedCount, &i.LatestRunModuleCount,
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

// BatchClearForcePush clears force_push=0 across multiple envoys in a single
// UPDATE ... WHERE id IN (...) AND force_push = 1. The trailing predicate
// means SQLite only writes rows that actually had it set, turning the common
// case (no envoys forced) into a zero-row no-op instead of N rewrites.
func (q *Queries) BatchClearForcePush(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("UPDATE envoys SET force_push = 0 WHERE id IN (%s) AND force_push = 1",
		strings.Join(placeholders, ","))
	_, err := q.db.ExecContext(ctx, query, args...)
	return err
}

// BatchClearRotatedSecrets resets pending_rotated_secrets='[]' across multiple
// envoys, skipping rows whose list is already empty.
func (q *Queries) BatchClearRotatedSecrets(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("UPDATE envoys SET pending_rotated_secrets = '[]' WHERE id IN (%s) AND pending_rotated_secrets != '[]'",
		strings.Join(placeholders, ","))
	_, err := q.db.ExecContext(ctx, query, args...)
	return err
}

// BatchClearEnvoyMaintenance clears maintenance_until=NULL across multiple
// envoys, skipping rows that are already NULL. Operator-set non-NULL values
// are written via the per-envoy UpdateEnvoyMaintenance — that set is empirically
// tiny (admin-driven, rare) so the CASE-WHEN batching for divergent values isn't
// worth the complexity.
func (q *Queries) BatchClearEnvoyMaintenance(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("UPDATE envoys SET maintenance_until = NULL WHERE id IN (%s) AND maintenance_until IS NOT NULL",
		strings.Join(placeholders, ","))
	_, err := q.db.ExecContext(ctx, query, args...)
	return err
}

// BatchUpdateAgentVersion sets agent_version for many envoys that all report
// the same version. Callers in the flusher group dirty envoys by AgentVersion
// value so each chunk's UPDATE writes one version across N rows in a single
// statement — at 10000-envoy stagger-up that's ~20 chunked statements instead
// of 10000 single-row UPDATEs serialized through the WAL writer mutex. The
// trailing `AND agent_version != ?` predicate makes the steady-state case
// (every envoy already has the version) a zero-row no-op.
func (q *Queries) BatchUpdateAgentVersion(ctx context.Context, ids []string, version string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+2)
	args = append(args, version) // SET agent_version = ?
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, version) // AND agent_version != ?
	query := fmt.Sprintf("UPDATE envoys SET agent_version = ? WHERE id IN (%s) AND agent_version != ?",
		strings.Join(placeholders, ","))
	_, err := q.db.ExecContext(ctx, query, args...)
	return err
}
