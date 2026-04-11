-- name: CreateSwarmSeedAllowEntry :one
INSERT INTO swarm_seed_allowlist (pattern, created_by)
VALUES (?, ?)
RETURNING id, pattern, created_at, created_by;

-- name: ListSwarmSeedAllowEntries :many
SELECT id, pattern, created_at, created_by
FROM swarm_seed_allowlist
ORDER BY created_at DESC;

-- name: DeleteSwarmSeedAllowEntry :exec
DELETE FROM swarm_seed_allowlist
WHERE id = ?;

-- name: ListSwarmSeedAllowPatterns :many
SELECT pattern
FROM swarm_seed_allowlist;
