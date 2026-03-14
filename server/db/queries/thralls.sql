-- name: CreateThrall :one
INSERT INTO thralls (hostname, ip_address, pubkey_pem)
VALUES (?, ?, ?)
RETURNING id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets;

-- name: GetThrallByID :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets
FROM thralls
WHERE id = ?;

-- name: GetThrallByHostname :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets
FROM thralls
WHERE hostname = ?;

-- name: ListThralls :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets
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
