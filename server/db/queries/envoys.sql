-- name: CreateEnvoy :one
INSERT INTO envoys (hostname, ip_address, pubkey_pem)
VALUES (?, ?, ?)
RETURNING id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, maintenance_until, agent_version, cert_spki_sha256;

-- name: GetEnvoyByID :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, maintenance_until, agent_version, cert_spki_sha256
FROM envoys
WHERE id = ?;

-- name: GetEnvoyByHostname :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, maintenance_until, agent_version, cert_spki_sha256
FROM envoys
WHERE hostname = ?;

-- name: GetActiveEnvoyByHostname :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, maintenance_until, agent_version, cert_spki_sha256
FROM envoys
WHERE hostname = ? AND revoked = 0;

-- name: ListEnvoys :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, maintenance_until, agent_version, cert_spki_sha256
FROM envoys
ORDER BY enrolled_at DESC;

-- name: ListActiveEnvoys :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, maintenance_until, agent_version, cert_spki_sha256
FROM envoys
WHERE revoked = 0
ORDER BY enrolled_at DESC;

-- name: ListActiveEnvoysPaged :many
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, maintenance_until, agent_version, cert_spki_sha256
FROM envoys
WHERE revoked = 0
  AND (sqlc.arg(after_hostname) = '' OR hostname > sqlc.arg(after_hostname))
  AND (sqlc.arg(search) = '' OR hostname LIKE '%' || sqlc.arg(search) || '%')
ORDER BY hostname
LIMIT sqlc.arg(page_limit);

-- name: ListEnvoysOffset :many
-- Offset/limit paginated list of every envoy (active + revoked).
-- Stable ordering: enrolled_at DESC then id ASC.
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, maintenance_until, agent_version, cert_spki_sha256
FROM envoys
ORDER BY enrolled_at DESC, id
LIMIT ? OFFSET ?;

-- name: ListActiveEnvoysOffset :many
-- Offset/limit paginated list of envoys with revoked = 0.
-- Stable ordering: enrolled_at DESC then id ASC.
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push, pending_rotated_secrets, maintenance_until, agent_version, cert_spki_sha256
FROM envoys
WHERE revoked = 0
ORDER BY enrolled_at DESC, id
LIMIT ? OFFSET ?;

-- name: CountActiveEnvoys :one
SELECT count(*) FROM envoys WHERE revoked = 0;

-- name: CountEnvoys :one
SELECT count(*) FROM envoys;

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

-- name: UpdateEnvoyIP :exec
UPDATE envoys
SET ip_address = ?
WHERE id = ? AND ip_address != ?;

-- name: UpdateEnvoyCertPin :exec
UPDATE envoys SET cert_spki_sha256 = ? WHERE id = ?;

-- name: ListRevokedEnvoyIDs :many
-- IDs of every revoked envoy. Loaded into the fleet index at boot so the
-- check-in verify path answers "revoked" without a DB read (rehydrate loads
-- only non-revoked envoys).
SELECT id FROM envoys WHERE revoked != 0;
