-- name: UpsertComplianceSnapshot :exec
INSERT INTO compliance_history (snapshot_date, framework, score_percent, satisfied, partial, not_satisfied, total_controls)
VALUES (date('now'), ?1, ?2, ?3, ?4, ?5, ?6)
ON CONFLICT(snapshot_date, framework) DO UPDATE SET
  score_percent  = excluded.score_percent,
  satisfied      = excluded.satisfied,
  partial        = excluded.partial,
  not_satisfied  = excluded.not_satisfied,
  total_controls = excluded.total_controls;

-- name: ListComplianceHistory :many
SELECT id, snapshot_date, framework, score_percent, satisfied, partial, not_satisfied, total_controls
FROM compliance_history
WHERE framework = ?1
ORDER BY snapshot_date DESC
LIMIT ?2;

-- name: ListComplianceHistoryAllFrameworks :many
SELECT id, snapshot_date, framework, score_percent, satisfied, partial, not_satisfied, total_controls
FROM compliance_history
ORDER BY snapshot_date DESC, framework
LIMIT ?;

-- name: PruneComplianceHistoryByAge :execrows
DELETE FROM compliance_history
WHERE snapshot_date < ?;
