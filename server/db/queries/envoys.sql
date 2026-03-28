-- name: CreateEnvoy :one
INSERT INTO envoys (hostname, ip_address, pubkey_pem)
VALUES (?, ?, ?)
RETURNING id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until, agent_version;

-- name: GetEnvoyByID :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until, agent_version
FROM envoys
WHERE id = ?;

-- name: GetEnvoyByHostname :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until, agent_version
FROM envoys
WHERE hostname = ?;

-- name: GetActiveEnvoyByHostname :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until, agent_version
FROM envoys
WHERE hostname = ? AND revoked = 0;

-- name: ListEnvoys :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until, agent_version
FROM envoys
ORDER BY enrolled_at DESC;

-- name: ListActiveEnvoys :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until, agent_version
FROM envoys
WHERE revoked = 0
ORDER BY enrolled_at DESC;

-- name: ListActiveEnvoysPaged :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until, agent_version
FROM envoys
WHERE revoked = 0
  AND (sqlc.arg(after_hostname) = '' OR hostname > sqlc.arg(after_hostname))
  AND (sqlc.arg(search) = '' OR hostname LIKE '%' || sqlc.arg(search) || '%')
ORDER BY hostname
LIMIT sqlc.arg(page_limit);

-- name: CountActiveEnvoys :one
SELECT count(*) FROM envoys WHERE revoked = 0;

-- name: RevokeEnvoy :exec
UPDATE envoys
SET revoked = true, revoked_at = datetime('now')
WHERE id = ?;

-- name: UpdateLastSeen :exec
UPDATE envoys
SET last_seen = datetime('now')
WHERE id = ?;

-- name: SetForcePush :exec
UPDATE envoys
SET force_push = true
WHERE id = ?;

-- name: ClearForcePush :exec
UPDATE envoys
SET force_push = false
WHERE id = ?;

-- name: DeleteEnvoy :exec
DELETE FROM envoys
WHERE id = ?;

-- name: UpdateEnvoyTags :exec
UPDATE envoys
SET tags = ?
WHERE id = ?;

-- name: UpdateEnvoyMaintenance :exec
UPDATE envoys
SET maintenance_until = ?
WHERE id = ?;

-- name: ClearEnvoyMaintenance :exec
UPDATE envoys
SET maintenance_until = NULL
WHERE id = ?;

-- name: UpdateAgentVersion :exec
UPDATE envoys
SET agent_version = ?
WHERE id = ? AND agent_version != ?;
