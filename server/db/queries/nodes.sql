-- name: CreateNode :one
INSERT INTO nodes (hostname, ip_address, pubkey_pem)
VALUES (?, ?, ?)
RETURNING id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push;

-- name: GetNodeByID :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM nodes
WHERE id = ?;

-- name: GetNodeByHostname :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM nodes
WHERE hostname = ?;

-- name: ListNodes :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM nodes
ORDER BY enrolled_at DESC;

-- name: RevokeNode :exec
UPDATE nodes
SET revoked = true, revoked_at = datetime('now')
WHERE id = ?;

-- name: UpdateLastSeen :exec
UPDATE nodes
SET last_seen = datetime('now')
WHERE id = ?;

-- name: SetForcePush :exec
UPDATE nodes
SET force_push = true
WHERE id = ?;

-- name: ClearForcePush :exec
UPDATE nodes
SET force_push = false
WHERE id = ?;

-- name: DeleteNode :exec
DELETE FROM nodes
WHERE id = ?;

-- name: ListNodesWithForcePush :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM nodes
WHERE force_push = true;
