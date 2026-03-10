package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// SetForcePushAllActive sets force_push=true on all non-revoked nodes in a
// single UPDATE, replacing the N+1 loop in ForcePush("all").
func (q *Queries) SetForcePushAllActive(ctx context.Context) (int64, error) {
	tag, err := q.db.Exec(ctx,
		"UPDATE nodes SET force_push = true WHERE revoked = false")
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// NodeWithLatestRun pairs a node with its most recent run's status and
// changed count. Used by GetComplianceSummary to eliminate the N+1 query.
type NodeWithLatestRun struct {
	Node
	LatestRunStatus       pgtype.Text
	LatestRunChangedCount pgtype.Int4
}

// ListNodesWithLatestRun returns all non-revoked nodes joined with their
// latest run in a single query (LATERAL JOIN), replacing N+1 per-node lookups.
func (q *Queries) ListNodesWithLatestRun(ctx context.Context) ([]NodeWithLatestRun, error) {
	const query = `
SELECT n.id, n.hostname, n.ip_address, n.pubkey_pem, n.enrolled_at,
       n.last_seen, n.revoked, n.revoked_at, n.force_push,
       r.status, r.changed_count
FROM nodes n
LEFT JOIN LATERAL (
    SELECT status, changed_count
    FROM runs
    WHERE node_id = n.id
    ORDER BY started_at DESC
    LIMIT 1
) r ON true
WHERE n.revoked = false
ORDER BY n.enrolled_at DESC`

	rows, err := q.db.Query(ctx, query)
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

// BatchUpdateLastSeen updates last_seen=now() for multiple node UUIDs in a
// single UPDATE ... WHERE id = ANY($1), replacing per-node loops.
func (q *Queries) BatchUpdateLastSeen(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	pgIDs := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		pgIDs[i] = pgtype.UUID{Bytes: id, Valid: true}
	}
	_, err := q.db.Exec(ctx,
		"UPDATE nodes SET last_seen = now() WHERE id = ANY($1)", pgIDs)
	return err
}
