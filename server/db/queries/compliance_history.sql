-- name: UpsertComplianceSnapshot :one
INSERT INTO compliance_history (recorded_at, total, compliant, drifted, failing, stale, unknown)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (recorded_at) DO UPDATE
SET total = EXCLUDED.total,
    compliant = EXCLUDED.compliant,
    drifted = EXCLUDED.drifted,
    failing = EXCLUDED.failing,
    stale = EXCLUDED.stale,
    unknown = EXCLUDED.unknown
RETURNING id, recorded_at, total, compliant, drifted, failing, stale, unknown;

-- name: ListComplianceHistory :many
SELECT id, recorded_at, total, compliant, drifted, failing, stale, unknown
FROM compliance_history
ORDER BY recorded_at DESC
LIMIT $1;
