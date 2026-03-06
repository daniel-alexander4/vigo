-- name: CreateSecretRotationEvent :one
INSERT INTO secret_rotation_events (secret_path, old_version, new_version, affected_clients)
VALUES ($1, $2, $3, $4)
RETURNING id, secret_path, old_version, new_version, rotated_at, affected_clients;

-- name: ListSecretRotationEvents :many
SELECT id, secret_path, old_version, new_version, rotated_at, affected_clients
FROM secret_rotation_events
ORDER BY rotated_at DESC
LIMIT $1;
