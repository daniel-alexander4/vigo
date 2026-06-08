-- name: UpsertComplianceSnapshot :exec
INSERT INTO compliance_history (snapshot_date, framework, score_percent, satisfied, waived, partial, not_satisfied, total_controls)
VALUES (date('now'), ?1, ?2, ?3, ?4, ?5, ?6, ?7)
ON CONFLICT(snapshot_date, framework) DO UPDATE SET
  score_percent  = excluded.score_percent,
  satisfied      = excluded.satisfied,
  waived         = excluded.waived,
  partial        = excluded.partial,
  not_satisfied  = excluded.not_satisfied,
  total_controls = excluded.total_controls;

-- name: ListComplianceHistory :many
SELECT id, snapshot_date, framework, score_percent, satisfied, waived, partial, not_satisfied, total_controls
FROM compliance_history
WHERE framework = ?1
ORDER BY snapshot_date DESC
LIMIT ?2;

-- name: ListComplianceHistoryAllFrameworks :many
SELECT id, snapshot_date, framework, score_percent, satisfied, waived, partial, not_satisfied, total_controls
FROM compliance_history
ORDER BY snapshot_date DESC, framework
LIMIT ?;

-- name: GetLatestComplianceSnapshotDate :one
SELECT snapshot_date
FROM compliance_history
ORDER BY snapshot_date DESC
LIMIT 1;

-- name: ListComplianceSnapshotByDate :many
SELECT framework, score_percent, satisfied, waived, partial, not_satisfied, total_controls
FROM compliance_history
WHERE snapshot_date = ?
ORDER BY framework;

-- name: PruneComplianceHistoryByAge :execrows
DELETE FROM compliance_history
WHERE rowid IN (
  SELECT t.rowid FROM compliance_history AS t
  WHERE t.snapshot_date < ?
  LIMIT ?
);
