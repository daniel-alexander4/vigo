-- name: PruneRunsByAge :execrows
DELETE FROM runs
WHERE started_at < ?
  AND status NOT IN ('running', 'pending');

-- name: PrunePushRunsByAge :execrows
DELETE FROM push_runs
WHERE created_at < ?
  AND status NOT IN ('running', 'pending');

-- name: PruneWorkflowRunsByAge :execrows
DELETE FROM workflow_runs
WHERE created_at < ?
  AND status NOT IN ('running', 'pending');

-- name: PruneConvergenceHistoryByAge :execrows
DELETE FROM convergence_history
WHERE recorded_at < ?;

-- name: PruneFileSnapshotsByAge :execrows
DELETE FROM file_snapshots
WHERE created_at < ?;
