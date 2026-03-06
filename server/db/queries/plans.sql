-- name: CreatePlan :one
INSERT INTO plans (client_id, modules)
VALUES ($1, $2)
RETURNING id, client_id, created_at, modules;

-- name: GetPlanByID :one
SELECT id, client_id, created_at, modules
FROM plans
WHERE id = $1;

-- name: ListPlansByClient :many
SELECT id, client_id, created_at, modules
FROM plans
WHERE client_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: CreatePlanResult :one
INSERT INTO plan_results (plan_id, module_name, resource_type, resource_name, planned_action)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, plan_id, module_name, resource_type, resource_name, planned_action;

-- name: ListPlanResults :many
SELECT id, plan_id, module_name, resource_type, resource_name, planned_action
FROM plan_results
WHERE plan_id = $1
ORDER BY id ASC;
