-- name: CreateRun :one
INSERT INTO runs (envoy_id, status, trigger, module_count)
VALUES (?, ?, ?, ?)
RETURNING id, envoy_id, started_at, finished_at, status, module_count, changed_count, failed_count, skipped_count, trigger;

-- name: GetRunByID :one
SELECT id, envoy_id, started_at, finished_at, status, module_count, changed_count, failed_count, skipped_count, trigger
FROM runs
WHERE id = ?;

-- name: ListRunsByEnvoy :many
SELECT id, envoy_id, started_at, finished_at, status, module_count, changed_count, failed_count, skipped_count, trigger
FROM runs
WHERE envoy_id = ?1
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
SELECT id, envoy_id, started_at, finished_at, status, module_count, changed_count, failed_count, skipped_count, trigger
FROM runs
ORDER BY started_at DESC
LIMIT ?;

-- name: ListRunResultsSince :many
SELECT rr.id, rr.run_id, rr.module_name, rr.resource_type, rr.resource_name,
       rr.action_taken, rr.changed, rr.error_message, rr.duration_ms, rr.created_at,
       r.envoy_id, r.status AS run_status, r.started_at AS run_started_at,
       t.hostname
FROM run_results rr
JOIN runs r ON r.id = rr.run_id
JOIN envoys t ON t.id = r.envoy_id
WHERE rr.created_at >= ?1
ORDER BY rr.created_at DESC
LIMIT ?2;

-- name: ListRunResultsSinceByEnvoy :many
SELECT rr.id, rr.run_id, rr.module_name, rr.resource_type, rr.resource_name,
       rr.action_taken, rr.changed, rr.error_message, rr.duration_ms, rr.created_at,
       r.envoy_id, r.status AS run_status, r.started_at AS run_started_at,
       t.hostname
FROM run_results rr
JOIN runs r ON r.id = rr.run_id
JOIN envoys t ON t.id = r.envoy_id
WHERE rr.created_at >= ?1 AND t.hostname = ?2
ORDER BY rr.created_at DESC
LIMIT ?3;

-- name: ListRunResultsByRunIDs :many
SELECT id, run_id, module_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms, created_at
FROM run_results
WHERE run_id IN (sqlc.slice('run_ids'))
ORDER BY run_id, created_at ASC;

-- name: ListFailedRunsForDate :many
SELECT id, envoy_id, started_at, finished_at, status, module_count, changed_count, failed_count, skipped_count, trigger
FROM runs
WHERE date(started_at) = ?1 AND status IN ('failure', 'partial')
ORDER BY started_at ASC;

-- name: GetNextSuccessfulRun :one
SELECT id, envoy_id, started_at, finished_at, status, module_count, changed_count, failed_count, skipped_count, trigger
FROM runs
WHERE envoy_id = ?1 AND started_at > ?2 AND status = 'success'
ORDER BY started_at ASC
LIMIT 1;

-- name: EnvoysWithModule :many
-- Find active envoys that have run a specific module, with the most recent run time.
-- Used by module retraction to show which envoys are affected when a module is removed.
SELECT e.id, e.hostname, MAX(r.started_at) AS last_run
FROM run_results rr
JOIN runs r ON r.id = rr.run_id
JOIN envoys e ON e.id = r.envoy_id
WHERE rr.module_name = ?1
  AND r.status IN ('success', 'partial')
  AND e.revoked = 0
GROUP BY e.id, e.hostname
ORDER BY e.hostname;
