-- name: PruneRunsByAge :exec
DELETE FROM runs
WHERE started_at < ?
  AND status NOT IN ('running', 'pending');

-- name: PrunePushRunsByAge :exec
DELETE FROM push_runs
WHERE created_at < ?
  AND status NOT IN ('running', 'pending');

-- name: PruneWorkflowRunsByAge :exec
DELETE FROM workflow_runs
WHERE created_at < ?
  AND status NOT IN ('running', 'pending');

-- name: PruneConvergenceHistoryByAge :exec
DELETE FROM convergence_history
WHERE recorded_at < ?;

-- name: PruneFileSnapshotsByAge :exec
DELETE FROM file_snapshots
WHERE created_at < ?;
