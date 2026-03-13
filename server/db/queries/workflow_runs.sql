-- name: CreateWorkflowRun :one
INSERT INTO workflow_runs (workflow_name, status, total_steps, operator)
VALUES (?, 'pending', ?, ?)
RETURNING *;

-- name: GetWorkflowRun :one
SELECT * FROM workflow_runs WHERE id = ?;

-- name: ListWorkflowRuns :many
SELECT * FROM workflow_runs
ORDER BY created_at DESC
LIMIT ?;

-- name: UpdateWorkflowRunStatus :exec
UPDATE workflow_runs
SET status = ?2, current_step = ?3, started_at = ?4, finished_at = ?5, failure_reason = ?6
WHERE id = ?1;

-- name: CreateWorkflowStepResult :one
INSERT INTO workflow_step_results (workflow_run_id, step_index, step_name, task_run_id, status)
VALUES (?, ?, ?, ?, 'pending')
RETURNING *;

-- name: GetWorkflowStepResult :one
SELECT * FROM workflow_step_results WHERE id = ?;

-- name: ListWorkflowStepResults :many
SELECT * FROM workflow_step_results
WHERE workflow_run_id = ?
ORDER BY step_index ASC;

-- name: UpdateWorkflowStepResult :exec
UPDATE workflow_step_results
SET status = ?2, task_run_id = ?3, started_at = ?4, finished_at = ?5
WHERE id = ?1;

-- name: FailInterruptedWorkflows :exec
UPDATE workflow_runs
SET status = 'failed', finished_at = datetime('now'), failure_reason = 'restart_interrupted'
WHERE status IN ('running', 'pending');
