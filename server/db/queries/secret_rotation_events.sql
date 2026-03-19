-- name: CreateSecretRotationEvent :one
INSERT INTO secret_rotation_events (secret_path, old_version, new_version, affected_envoys)
VALUES (?, ?, ?, ?)
RETURNING id, secret_path, old_version, new_version, rotated_at, affected_envoys;
