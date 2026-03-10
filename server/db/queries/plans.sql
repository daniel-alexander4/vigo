-- name: CreatePlan :one
INSERT INTO plans (node_id, modules)
VALUES (?, ?)
RETURNING id, node_id, created_at, modules;

-- name: GetPlanByID :one
SELECT id, node_id, created_at, modules
FROM plans
WHERE id = ?;

-- name: ListPlansByNode :many
SELECT id, node_id, created_at, modules
FROM plans
WHERE node_id = ?1
ORDER BY created_at DESC
LIMIT ?2;

-- name: CreatePlanResult :one
INSERT INTO plan_results (plan_id, module_name, resource_type, resource_name, planned_action)
VALUES (?, ?, ?, ?, ?)
RETURNING id, plan_id, module_name, resource_type, resource_name, planned_action;

-- name: ListPlanResults :many
SELECT id, plan_id, module_name, resource_type, resource_name, planned_action
FROM plan_results
WHERE plan_id = ?
ORDER BY id ASC;
