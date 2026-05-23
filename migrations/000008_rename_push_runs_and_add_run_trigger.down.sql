-- Reverse of 000008: drop runs.trigger and rename task_runs back to push_runs.

ALTER TABLE runs DROP COLUMN trigger;

DROP INDEX IF EXISTS idx_task_runs_status;
DROP INDEX IF EXISTS idx_task_runs_created_at;
DROP INDEX IF EXISTS idx_task_run_targets_task_run_id;
DROP INDEX IF EXISTS idx_task_run_targets_envoy_id;

ALTER TABLE workflow_step_results RENAME COLUMN task_run_id TO push_run_id;
ALTER TABLE task_run_targets RENAME COLUMN task_run_id TO push_run_id;
ALTER TABLE task_run_targets RENAME TO push_run_targets;
ALTER TABLE task_runs RENAME TO push_runs;

CREATE INDEX IF NOT EXISTS idx_push_runs_status ON push_runs (status);
CREATE INDEX IF NOT EXISTS idx_push_runs_created_at ON push_runs (created_at);
CREATE INDEX IF NOT EXISTS idx_push_run_targets_push_run_id ON push_run_targets (push_run_id);
CREATE INDEX IF NOT EXISTS idx_push_run_targets_envoy_id ON push_run_targets (envoy_id);
