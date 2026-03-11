-- name: CreateRun :one
INSERT INTO runs (thrall_id, status, module_count)
VALUES (?, ?, ?)
RETURNING id, thrall_id, started_at, finished_at, status, module_count, changed_count, failed_count, skipped_count;

-- name: GetRunByID :one
SELECT id, thrall_id, started_at, finished_at, status, module_count, changed_count, failed_count, skipped_count
FROM runs
WHERE id = ?;

-- name: ListRunsByThrall :many
SELECT id, thrall_id, started_at, finished_at, status, module_count, changed_count, failed_count, skipped_count
FROM runs
WHERE thrall_id = ?1
ORDER BY started_at DESC
LIMIT ?2;

-- name: FinishRun :exec
UPDATE runs
SET finished_at = datetime('now'), status = ?2, changed_count = ?3, failed_count = ?4, skipped_count = ?5
WHERE id = ?1;

-- name: CreateRunResult :one
INSERT INTO run_results (run_id, module_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, run_id, module_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms, created_at;

-- name: ListRunResults :many
SELECT id, run_id, module_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms, created_at
FROM run_results
WHERE run_id = ?
ORDER BY created_at ASC;

-- name: ListAllRuns :many
SELECT id, thrall_id, started_at, finished_at, status, module_count, changed_count, failed_count, skipped_count
FROM runs
ORDER BY started_at DESC
LIMIT ?;
