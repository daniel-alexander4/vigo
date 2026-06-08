-- name: PruneRunsByAge :execrows
DELETE FROM runs
WHERE rowid IN (
  SELECT t.rowid FROM runs AS t
  WHERE t.started_at < ?
    AND t.status NOT IN ('running', 'pending')
  LIMIT ?
);

-- name: PruneTaskRunsByAge :execrows
DELETE FROM task_runs
WHERE rowid IN (
  SELECT t.rowid FROM task_runs AS t
  WHERE t.created_at < ?
    AND t.status NOT IN ('running', 'pending')
  LIMIT ?
);

-- name: PruneWorkflowRunsByAge :execrows
DELETE FROM workflow_runs
WHERE rowid IN (
  SELECT t.rowid FROM workflow_runs AS t
  WHERE t.created_at < ?
    AND t.status NOT IN ('running', 'pending')
  LIMIT ?
);

-- name: PruneConvergenceHistoryByAge :execrows
DELETE FROM convergence_history
WHERE rowid IN (
  SELECT t.rowid FROM convergence_history AS t
  WHERE t.recorded_at < ?
  LIMIT ?
);

-- name: PruneFileSnapshotsByAge :execrows
DELETE FROM file_snapshots
WHERE rowid IN (
  SELECT t.rowid FROM file_snapshots AS t
  WHERE t.created_at < ?
  LIMIT ?
);
