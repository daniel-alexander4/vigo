-- Spanner Slice 2A admissions CRDT queries (ADR-026 + the spanner-redesign
-- plan). spanner_bolts is a peer-equal LWW-Map keyed by pubkey;
-- spanner_consumed_tokens is a G-Set keyed by token_hash. An admission that
-- consumed a join token carries that token's hash in
-- spanner_bolts.consumed_token_hash, a nullable FK to
-- spanner_consumed_tokens.token_hash. Callers admitting a bolt via a token
-- wrap ConsumeToken + InsertBolt in one sql.Tx via Queries.WithTx, the token
-- INSERT ordered first because it is the FK target. MergeBoltLWW is the
-- gossip-replication entry point and encodes absolute revoke-wins: a revoked
-- row never returns to live, since pubkey rotation is forbidden by the
-- canonical plan.

-- name: InsertBolt :exec
INSERT INTO spanner_bolts (
    pubkey, hostname, admitted_at, admitted_by, consumed_token_hash,
    revoked_at, revoked_by, last_snapshot_at, pattern_claims, signature
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetBolt :one
SELECT pubkey, hostname, admitted_at, admitted_by, consumed_token_hash,
       revoked_at, revoked_by, last_snapshot_at, pattern_claims, signature
FROM spanner_bolts
WHERE pubkey = ?;

-- name: ListBolts :many
SELECT pubkey, hostname, admitted_at, admitted_by, consumed_token_hash,
       revoked_at, revoked_by, last_snapshot_at, pattern_claims, signature
FROM spanner_bolts
ORDER BY pubkey;

-- name: ListLiveBolts :many
SELECT pubkey, hostname, admitted_at, admitted_by, consumed_token_hash,
       revoked_at, revoked_by, last_snapshot_at, pattern_claims, signature
FROM spanner_bolts
WHERE revoked_at IS NULL
ORDER BY pubkey;

-- name: RevokeBolt :exec
UPDATE spanner_bolts
SET revoked_at = ?2, revoked_by = ?3, signature = ?4
WHERE pubkey = ?1
  AND revoked_at IS NULL;

-- name: MergeBoltLWW :execrows
INSERT INTO spanner_bolts (
    pubkey, hostname, admitted_at, admitted_by, consumed_token_hash,
    revoked_at, revoked_by, last_snapshot_at, pattern_claims, signature
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(pubkey) DO UPDATE SET
    hostname            = excluded.hostname,
    admitted_at         = excluded.admitted_at,
    admitted_by         = excluded.admitted_by,
    consumed_token_hash = excluded.consumed_token_hash,
    revoked_at          = excluded.revoked_at,
    revoked_by          = excluded.revoked_by,
    last_snapshot_at    = excluded.last_snapshot_at,
    pattern_claims      = excluded.pattern_claims,
    signature           = excluded.signature
WHERE (excluded.admitted_at > spanner_bolts.admitted_at
       OR (excluded.admitted_at = spanner_bolts.admitted_at
           AND excluded.revoked_at IS NOT NULL
           AND spanner_bolts.revoked_at IS NULL))
  AND NOT (spanner_bolts.revoked_at IS NOT NULL
           AND excluded.revoked_at IS NULL);

-- name: ConsumeToken :exec
INSERT INTO spanner_consumed_tokens (
    token_hash, consumed_at, consumed_by, admitted_pubkey, expires_at
) VALUES (?, ?, ?, ?, ?);

-- name: MergeConsumedToken :execrows
INSERT INTO spanner_consumed_tokens (
    token_hash, consumed_at, consumed_by, admitted_pubkey, expires_at
) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(token_hash) DO NOTHING;

-- name: IsTokenConsumed :one
SELECT EXISTS (SELECT 1 FROM spanner_consumed_tokens WHERE token_hash = ?) AS consumed;

-- name: GetConsumedToken :one
SELECT token_hash, consumed_at, consumed_by, admitted_pubkey, expires_at
FROM spanner_consumed_tokens
WHERE token_hash = ?;
