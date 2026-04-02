-- name: ListConvergenceHistory :many
SELECT id, recorded_at, total, converged, relapsed, diverged, failed, offline, no_data
FROM convergence_history
ORDER BY recorded_at DESC
LIMIT ?;

-- name: GetConvergenceHistoryForDate :one
SELECT id, recorded_at, total, converged, relapsed, diverged, failed, offline, no_data
FROM convergence_history
WHERE recorded_at = ?
LIMIT 1;

-- name: ListConvergenceHistoryRange :many
SELECT id, recorded_at, total, converged, relapsed, diverged, failed, offline, no_data
FROM convergence_history
WHERE recorded_at BETWEEN ?1 AND ?2
ORDER BY recorded_at DESC;

-- name: UpsertConvergenceSnapshot :exec
INSERT INTO convergence_history (recorded_at, total, converged, relapsed, diverged, failed, offline, no_data)
VALUES (date('now'), ?1, ?2, ?3, ?4, ?5, ?6, ?7)
ON CONFLICT(recorded_at) DO UPDATE SET
  total        = excluded.total,
  converged    = excluded.converged,
  relapsed     = excluded.relapsed,
  diverged     = excluded.diverged,
  failed       = excluded.failed,
  offline = excluded.offline,
  no_data      = excluded.no_data;
