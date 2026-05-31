-- name: CreatePlan :one
INSERT INTO plans (envoy_id, configcrates)
VALUES (?, ?)
RETURNING id, envoy_id, created_at, configcrates;

-- name: GetPlanByID :one
SELECT id, envoy_id, created_at, configcrates
FROM plans
WHERE id = ?;

-- name: CreatePlanResult :one
INSERT INTO plan_results (plan_id, configcrate_name, resource_type, resource_name, planned_action)
VALUES (?, ?, ?, ?, ?)
RETURNING id, plan_id, configcrate_name, resource_type, resource_name, planned_action;

-- name: ListPlanResults :many
SELECT id, plan_id, configcrate_name, resource_type, resource_name, planned_action
FROM plan_results
WHERE plan_id = ?
ORDER BY id ASC;
