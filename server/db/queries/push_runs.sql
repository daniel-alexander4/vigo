-- name: CreatePushRun :one
INSERT INTO push_runs (command, target_glob, operator, status, total_targets, timeout_secs, batch_size, max_failures)
VALUES (?, ?, ?, 'pending', ?, ?, ?, ?)
RETURNING *;

-- name: GetPushRun :one
SELECT * FROM push_runs WHERE id = ?;

-- name: ListPushRuns :many
SELECT * FROM push_runs
ORDER BY created_at DESC
LIMIT ?;

-- name: UpdatePushRunStatus :exec
UPDATE push_runs
SET status = ?2, started_at = ?3, finished_at = ?4
WHERE id = ?1;

-- name: CreatePushRunTarget :one
INSERT INTO push_run_targets (push_run_id, envoy_id, hostname, status)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: ListPushRunTargets :many
SELECT * FROM push_run_targets
WHERE push_run_id = ?
ORDER BY hostname ASC;

-- name: UpdatePushRunTarget :exec
UPDATE push_run_targets
SET status = ?2, exit_code = ?3, stdout = ?4, stderr = ?5, duration_ms = ?6, error_msg = ?7, dispatched_at = ?8, finished_at = ?9
WHERE id = ?1;

-- name: CountPushRunTargetsByStatus :many
SELECT status, COUNT(*) as count
FROM push_run_targets
WHERE push_run_id = ?
GROUP BY status;

-- name: CancelPushRun :exec
UPDATE push_runs
SET status = 'cancelled', finished_at = CURRENT_TIMESTAMP
WHERE id = ? AND status IN ('pending', 'running');

-- name: CancelPushRunTargets :exec
UPDATE push_run_targets
SET status = 'cancelled'
WHERE push_run_id = ? AND status IN ('pending', 'dispatched');

-- name: CancelAllRunningPushRuns :exec
UPDATE push_runs
SET status = 'cancelled', finished_at = CURRENT_TIMESTAMP
WHERE status IN ('pending', 'running');

-- name: ListRunningPushRunIDs :many
SELECT id FROM push_runs WHERE status IN ('pending', 'running');

-- name: TimeoutExpiredPushRuns :many
SELECT id FROM push_runs
WHERE status IN ('pending', 'running')
  AND datetime(created_at, '+' || timeout_secs || ' seconds') < datetime('now');

-- name: TimeoutPushRunTargets :exec
UPDATE push_run_targets
SET status = 'timeout'
WHERE push_run_id = ? AND status IN ('pending', 'dispatched');

-- name: TimeoutPushRun :exec
UPDATE push_runs
SET status = 'timeout', finished_at = CURRENT_TIMESTAMP
WHERE id = ? AND status IN ('pending', 'running');
