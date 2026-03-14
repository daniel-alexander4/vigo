-- name: ListComplianceHistory :many
SELECT id, recorded_at, total, compliant, drifted, failing, stale, unknown
FROM compliance_history
ORDER BY recorded_at DESC
LIMIT ?;

-- name: UpsertComplianceSnapshot :exec
INSERT INTO compliance_history (recorded_at, total, compliant, drifted, failing, stale, unknown)
VALUES (date('now'), ?1, ?2, ?3, ?4, ?5, ?6)
ON CONFLICT(recorded_at) DO UPDATE SET
  total     = excluded.total,
  compliant = excluded.compliant,
  drifted   = excluded.drifted,
  failing   = excluded.failing,
  stale     = excluded.stale,
  unknown   = excluded.unknown;
