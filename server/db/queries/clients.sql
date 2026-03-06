-- name: CreateClient :one
INSERT INTO clients (hostname, ip_address, pubkey_pem)
VALUES ($1, $2, $3)
RETURNING id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push;

-- name: GetClientByID :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM clients
WHERE id = $1;

-- name: GetClientByHostname :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM clients
WHERE hostname = $1;

-- name: ListClients :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM clients
ORDER BY enrolled_at DESC;

-- name: RevokeClient :exec
UPDATE clients
SET revoked = true, revoked_at = now()
WHERE id = $1;

-- name: UpdateLastSeen :exec
UPDATE clients
SET last_seen = now()
WHERE id = $1;

-- name: SetForcePush :exec
UPDATE clients
SET force_push = true
WHERE id = $1;

-- name: ClearForcePush :exec
UPDATE clients
SET force_push = false
WHERE id = $1;

-- name: ListClientsWithForcePush :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
FROM clients
WHERE force_push = true;
