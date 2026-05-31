-- name: CreateEnvPromotion :one
INSERT INTO env_promotions (configcrate_name, from_env, to_env, diff)
VALUES (?, ?, ?, ?)
RETURNING id, configcrate_name, from_env, to_env, diff, promoted_at;
