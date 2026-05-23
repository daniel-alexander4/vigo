-- 000008: carry forward two schema changes that were briefly (and wrongly)
-- applied by editing 000001 in place — push_runs/push_run_targets renamed to
-- task_runs/task_run_targets (commit 1a2dc45b) and the runs.trigger column
-- (commit 2b64ad08). 000001 has been restored to its as-released shape; this
-- forward migration is how every database — fresh or already at version 7 —
-- converges to the current schema.

ALTER TABLE push_runs RENAME TO task_runs;
ALTER TABLE push_run_targets RENAME TO task_run_targets;
ALTER TABLE task_run_targets RENAME COLUMN push_run_id TO task_run_id;
ALTER TABLE workflow_step_results RENAME COLUMN push_run_id TO task_run_id;

DROP INDEX IF EXISTS idx_push_runs_status;
DROP INDEX IF EXISTS idx_push_runs_created_at;
DROP INDEX IF EXISTS idx_push_run_targets_push_run_id;
DROP INDEX IF EXISTS idx_push_run_targets_envoy_id;
CREATE INDEX IF NOT EXISTS idx_task_runs_status ON task_runs (status);
CREATE INDEX IF NOT EXISTS idx_task_runs_created_at ON task_runs (created_at);
CREATE INDEX IF NOT EXISTS idx_task_run_targets_task_run_id ON task_run_targets (task_run_id);
CREATE INDEX IF NOT EXISTS idx_task_run_targets_envoy_id ON task_run_targets (envoy_id);

ALTER TABLE runs ADD COLUMN trigger TEXT NOT NULL DEFAULT 'scheduled' CHECK (trigger IN ('scheduled', 'force-push'));
