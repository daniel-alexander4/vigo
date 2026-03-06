-- name: CreateEnvPromotion :one
INSERT INTO env_promotions (module_name, from_env, to_env, diff)
VALUES ($1, $2, $3, $4)
RETURNING id, module_name, from_env, to_env, diff, promoted_at;

-- name: ListEnvPromotions :many
SELECT id, module_name, from_env, to_env, diff, promoted_at
FROM env_promotions
ORDER BY promoted_at DESC
LIMIT $1;
