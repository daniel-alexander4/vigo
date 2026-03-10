package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SetForcePushAllActive sets force_push=1 on all non-revoked nodes in a
// single UPDATE, replacing the N+1 loop in ForcePush("all").
func (q *Queries) SetForcePushAllActive(ctx context.Context) (int64, error) {
	result, err := q.db.ExecContext(ctx,
		"UPDATE nodes SET force_push = 1 WHERE revoked = 0")
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// NodeWithLatestRun pairs a node with its most recent run's status and
// changed count. Used by GetComplianceSummary to eliminate the N+1 query.
type NodeWithLatestRun struct {
	Node
	LatestRunStatus       sql.NullString
	LatestRunChangedCount sql.NullInt64
}

// ListNodesWithLatestRun returns all non-revoked nodes joined with their
// latest run in a single query (correlated subqueries), replacing N+1
// per-node lookups.
func (q *Queries) ListNodesWithLatestRun(ctx context.Context) ([]NodeWithLatestRun, error) {
	const query = `
SELECT n.id, n.hostname, n.ip_address, n.pubkey_pem, n.enrolled_at,
       n.last_seen, n.revoked, n.revoked_at, n.force_push,
       (SELECT status FROM runs WHERE node_id = n.id ORDER BY started_at DESC LIMIT 1),
       (SELECT changed_count FROM runs WHERE node_id = n.id ORDER BY started_at DESC LIMIT 1)
FROM nodes n
WHERE n.revoked = 0
ORDER BY n.enrolled_at DESC`

	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []NodeWithLatestRun
	for rows.Next() {
		var i NodeWithLatestRun
		if err := rows.Scan(
			&i.ID, &i.Hostname, &i.IpAddress, &i.PubkeyPem,
			&i.EnrolledAt, &i.LastSeen, &i.Revoked, &i.RevokedAt, &i.ForcePush,
			&i.LatestRunStatus, &i.LatestRunChangedCount,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// BatchUpdateLastSeen updates last_seen=datetime('now') for multiple node IDs
// in a single UPDATE ... WHERE id IN (...), replacing per-node loops.
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
	query := fmt.Sprintf("UPDATE nodes SET last_seen = datetime('now') WHERE id IN (%s)",
		strings.Join(placeholders, ","))
	_, err := q.db.ExecContext(ctx, query, args...)
	return err
}
