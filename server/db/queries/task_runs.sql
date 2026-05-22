-- name: CreateTaskRun :one
INSERT INTO task_runs (command, target_glob, operator, status, total_targets, timeout_secs, batch_size, max_failures)
VALUES (?, ?, ?, 'pending', ?, ?, ?, ?)
RETURNING *;

-- name: GetTaskRun :one
SELECT * FROM task_runs WHERE id = ?;

-- name: ListTaskRuns :many
SELECT * FROM task_runs
ORDER BY created_at DESC
LIMIT ?;

-- name: UpdateTaskRunStatus :exec
UPDATE task_runs
SET status = ?2, started_at = ?3, finished_at = ?4
WHERE id = ?1;

-- name: CreateTaskRunTarget :one
INSERT INTO task_run_targets (task_run_id, envoy_id, hostname, status)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: ListTaskRunTargets :many
SELECT * FROM task_run_targets
WHERE task_run_id = ?
ORDER BY hostname ASC;

-- name: UpdateTaskRunTarget :exec
UPDATE task_run_targets
SET status = ?2, exit_code = ?3, stdout = ?4, stderr = ?5, duration_ms = ?6, error_msg = ?7, dispatched_at = ?8, finished_at = ?9
WHERE id = ?1;

-- name: CountTaskRunTargetsByStatus :many
SELECT status, COUNT(*) as count
FROM task_run_targets
WHERE task_run_id = ?
GROUP BY status;

-- name: CancelTaskRun :exec
UPDATE task_runs
SET status = 'cancelled', finished_at = CURRENT_TIMESTAMP
WHERE id = ? AND status IN ('pending', 'running');

-- name: CancelTaskRunTargets :exec
UPDATE task_run_targets
SET status = 'cancelled'
WHERE task_run_id = ? AND status IN ('pending', 'dispatched');

-- name: CancelAllRunningTaskRuns :exec
UPDATE task_runs
SET status = 'cancelled', finished_at = CURRENT_TIMESTAMP
WHERE status IN ('pending', 'running');

-- name: ListRunningTaskRunIDs :many
SELECT id FROM task_runs WHERE status IN ('pending', 'running');

-- name: TimeoutExpiredTaskRuns :many
SELECT id FROM task_runs
WHERE status IN ('pending', 'running')
  AND datetime(created_at, '+' || timeout_secs || ' seconds') < datetime('now');

-- name: TimeoutTaskRunTargets :exec
UPDATE task_run_targets
SET status = 'timeout'
WHERE task_run_id = ? AND status IN ('pending', 'dispatched');

-- name: TimeoutTaskRun :exec
UPDATE task_runs
SET status = 'timeout', finished_at = CURRENT_TIMESTAMP
WHERE id = ? AND status IN ('pending', 'running');
