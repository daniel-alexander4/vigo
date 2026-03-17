-- name: CreateThrall :one
INSERT INTO thralls (hostname, ip_address, pubkey_pem)
VALUES (?, ?, ?)
RETURNING id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until;

-- name: GetThrallByID :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until
FROM thralls
WHERE id = ?;

-- name: GetThrallByHostname :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until
FROM thralls
WHERE hostname = ?;

-- name: GetActiveThrallByHostname :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until
FROM thralls
WHERE hostname = ? AND revoked = 0;

-- name: ListThralls :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, tags, maintenance_until
FROM thralls
ORDER BY enrolled_at DESC;

-- name: RevokeThrall :exec
UPDATE thralls
SET revoked = true, revoked_at = datetime('now')
WHERE id = ?;

-- name: UpdateLastSeen :exec
UPDATE thralls
SET last_seen = datetime('now')
WHERE id = ?;

-- name: SetForcePush :exec
UPDATE thralls
SET force_push = true
WHERE id = ?;

-- name: ClearForcePush :exec
UPDATE thralls
SET force_push = false
WHERE id = ?;

-- name: DeleteThrall :exec
DELETE FROM thralls
WHERE id = ?;

-- name: UpdateThrallTags :exec
UPDATE thralls
SET tags = ?
WHERE id = ?;

-- name: UpdateThrallMaintenance :exec
UPDATE thralls
SET maintenance_until = ?
WHERE id = ?;

-- name: ClearThrallMaintenance :exec
UPDATE thralls
SET maintenance_until = NULL
WHERE id = ?;
