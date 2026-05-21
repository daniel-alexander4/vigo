-- Spanner Slice 2A admissions CRDT queries (ADR-026). spanner_bolts is a
-- peer-equal LWW-Map keyed by pubkey. spanner_consumed_tokens is a G-Set with
-- an FK to spanner_bolts enforcing the atomic admission+consume invariant.
-- Callers wrap InsertBolt + ConsumeToken in one sql.Tx via Queries.WithTx
-- so both land or neither does. MergeBoltLWW is the gossip-replication
-- entry point and encodes absolute revoke-wins: a revoked row never returns
-- to live, even with a strictly newer updated_at, since pubkey rotation is
-- forbidden by the canonical plan.

-- name: InsertBolt :exec
INSERT INTO spanner_bolts (
    pubkey, spanner_id, hostname, patterns,
    revoked_at, revoked_by, updated_at, updated_by, signature
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetBolt :one
SELECT pubkey, spanner_id, hostname, patterns, revoked_at, revoked_by,
       updated_at, updated_by, signature
FROM spanner_bolts
WHERE pubkey = ?;

-- name: ListBolts :many
SELECT pubkey, spanner_id, hostname, patterns, revoked_at, revoked_by,
       updated_at, updated_by, signature
FROM spanner_bolts
WHERE spanner_id = ?
ORDER BY pubkey;

-- name: ListLiveBolts :many
SELECT pubkey, spanner_id, hostname, patterns, revoked_at, revoked_by,
       updated_at, updated_by, signature
FROM spanner_bolts
WHERE spanner_id = ? AND revoked_at IS NULL
ORDER BY pubkey;

-- name: RevokeBolt :exec
UPDATE spanner_bolts
SET revoked_at = ?2, revoked_by = ?3, updated_at = ?4, updated_by = ?5, signature = ?6
WHERE pubkey = ?1
  AND revoked_at IS NULL;

-- name: MergeBoltLWW :execrows
INSERT INTO spanner_bolts (
    pubkey, spanner_id, hostname, patterns,
    revoked_at, revoked_by, updated_at, updated_by, signature
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(pubkey) DO UPDATE SET
    spanner_id  = excluded.spanner_id,
    hostname   = excluded.hostname,
    patterns   = excluded.patterns,
    revoked_at = excluded.revoked_at,
    revoked_by = excluded.revoked_by,
    updated_at = excluded.updated_at,
    updated_by = excluded.updated_by,
    signature  = excluded.signature
WHERE (excluded.updated_at > spanner_bolts.updated_at
       OR (excluded.updated_at = spanner_bolts.updated_at
           AND excluded.revoked_at IS NOT NULL
           AND spanner_bolts.revoked_at IS NULL))
  AND NOT (spanner_bolts.revoked_at IS NOT NULL
           AND excluded.revoked_at IS NULL);

-- name: ConsumeToken :exec
INSERT INTO spanner_consumed_tokens (
    token_hash, spanner_id, consumed_at, consumed_by_bolt_pubkey, signature
) VALUES (?, ?, ?, ?, ?);

-- name: IsTokenConsumed :one
SELECT EXISTS (SELECT 1 FROM spanner_consumed_tokens WHERE token_hash = ?) AS consumed;

-- name: GetConsumedToken :one
SELECT token_hash, spanner_id, consumed_at, consumed_by_bolt_pubkey, signature
FROM spanner_consumed_tokens
WHERE token_hash = ?;
