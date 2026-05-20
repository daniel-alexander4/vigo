-- Tungol Slice 2A admissions CRDT queries (ADR-026). tungol_steorras is a
-- peer-equal LWW-Map keyed by pubkey. tungol_consumed_tokens is a G-Set with
-- an FK to tungol_steorras enforcing the atomic admission+consume invariant.
-- Callers wrap InsertSteorra + ConsumeToken in one sql.Tx via Queries.WithTx
-- so both land or neither does. MergeSteorraLWW is the gossip-replication
-- entry point and encodes absolute revoke-wins: a revoked row never returns
-- to live, even with a strictly newer updated_at, since pubkey rotation is
-- forbidden by the canonical plan.

-- name: InsertSteorra :exec
INSERT INTO tungol_steorras (
    pubkey, tungol_id, hostname, patterns,
    revoked_at, revoked_by, updated_at, updated_by, signature
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSteorra :one
SELECT pubkey, tungol_id, hostname, patterns, revoked_at, revoked_by,
       updated_at, updated_by, signature
FROM tungol_steorras
WHERE pubkey = ?;

-- name: ListSteorras :many
SELECT pubkey, tungol_id, hostname, patterns, revoked_at, revoked_by,
       updated_at, updated_by, signature
FROM tungol_steorras
WHERE tungol_id = ?
ORDER BY pubkey;

-- name: ListLiveSteorras :many
SELECT pubkey, tungol_id, hostname, patterns, revoked_at, revoked_by,
       updated_at, updated_by, signature
FROM tungol_steorras
WHERE tungol_id = ? AND revoked_at IS NULL
ORDER BY pubkey;

-- name: RevokeSteorra :exec
UPDATE tungol_steorras
SET revoked_at = ?2, revoked_by = ?3, updated_at = ?4, updated_by = ?5, signature = ?6
WHERE pubkey = ?1
  AND revoked_at IS NULL;

-- name: MergeSteorraLWW :execrows
INSERT INTO tungol_steorras (
    pubkey, tungol_id, hostname, patterns,
    revoked_at, revoked_by, updated_at, updated_by, signature
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(pubkey) DO UPDATE SET
    tungol_id  = excluded.tungol_id,
    hostname   = excluded.hostname,
    patterns   = excluded.patterns,
    revoked_at = excluded.revoked_at,
    revoked_by = excluded.revoked_by,
    updated_at = excluded.updated_at,
    updated_by = excluded.updated_by,
    signature  = excluded.signature
WHERE (excluded.updated_at > tungol_steorras.updated_at
       OR (excluded.updated_at = tungol_steorras.updated_at
           AND excluded.revoked_at IS NOT NULL
           AND tungol_steorras.revoked_at IS NULL))
  AND NOT (tungol_steorras.revoked_at IS NOT NULL
           AND excluded.revoked_at IS NULL);

-- name: ConsumeToken :exec
INSERT INTO tungol_consumed_tokens (
    token_hash, tungol_id, consumed_at, consumed_by_steorra_pubkey, signature
) VALUES (?, ?, ?, ?, ?);

-- name: IsTokenConsumed :one
SELECT EXISTS (SELECT 1 FROM tungol_consumed_tokens WHERE token_hash = ?) AS consumed;

-- name: GetConsumedToken :one
SELECT token_hash, tungol_id, consumed_at, consumed_by_steorra_pubkey, signature
FROM tungol_consumed_tokens
WHERE token_hash = ?;
