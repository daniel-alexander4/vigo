-- name: CreateNode :one
INSERT INTO nodes (hostname, ip_address, pubkey_pem)
VALUES ($1, $2, $3)
RETURNING id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push;

-- name: GetNodeByID :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM nodes
WHERE id = $1;

-- name: GetNodeByHostname :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM nodes
WHERE hostname = $1;

-- name: ListNodes :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM nodes
ORDER BY enrolled_at DESC;

-- name: RevokeNode :exec
UPDATE nodes
SET revoked = true, revoked_at = now()
WHERE id = $1;

-- name: UpdateLastSeen :exec
UPDATE nodes
SET last_seen = now()
WHERE id = $1;

-- name: SetForcePush :exec
UPDATE nodes
SET force_push = true
WHERE id = $1;

-- name: ClearForcePush :exec
UPDATE nodes
SET force_push = false
WHERE id = $1;

-- name: ListNodesWithForcePush :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM nodes
WHERE force_push = true;
