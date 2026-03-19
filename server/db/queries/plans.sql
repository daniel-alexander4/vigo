-- name: CreatePlan :one
INSERT INTO plans (envoy_id, modules)
VALUES (?, ?)
RETURNING id, envoy_id, created_at, modules;

-- name: GetPlanByID :one
SELECT id, envoy_id, created_at, modules
FROM plans
WHERE id = ?;

-- name: CreatePlanResult :one
INSERT INTO plan_results (plan_id, module_name, resource_type, resource_name, planned_action)
VALUES (?, ?, ?, ?, ?)
RETURNING id, plan_id, module_name, resource_type, resource_name, planned_action;

-- name: ListPlanResults :many
SELECT id, plan_id, module_name, resource_type, resource_name, planned_action
FROM plan_results
WHERE plan_id = ?
ORDER BY id ASC;
