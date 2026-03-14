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

-- name: ListRunResultsSince :many
SELECT rr.id, rr.run_id, rr.module_name, rr.resource_type, rr.resource_name,
       rr.action_taken, rr.changed, rr.error_message, rr.duration_ms, rr.created_at,
       r.thrall_id, r.status AS run_status, r.started_at AS run_started_at,
       t.hostname
FROM run_results rr
JOIN runs r ON r.id = rr.run_id
JOIN thralls t ON t.id = r.thrall_id
WHERE rr.created_at >= ?1
ORDER BY rr.created_at DESC
LIMIT ?2;

-- name: ListRunResultsSinceByThrall :many
SELECT rr.id, rr.run_id, rr.module_name, rr.resource_type, rr.resource_name,
       rr.action_taken, rr.changed, rr.error_message, rr.duration_ms, rr.created_at,
       r.thrall_id, r.status AS run_status, r.started_at AS run_started_at,
       t.hostname
FROM run_results rr
JOIN runs r ON r.id = rr.run_id
JOIN thralls t ON t.id = r.thrall_id
WHERE rr.created_at >= ?1 AND t.hostname = ?2
ORDER BY rr.created_at DESC
LIMIT ?3;
