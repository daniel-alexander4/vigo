-- name: InsertAPIToken :one
INSERT INTO api_tokens (user_id, name, token_hash, expires_at)
VALUES (?, ?, ?, ?)
RETURNING id, user_id, name, token_hash, created_at, last_used_at, expires_at;

-- name: ListAPITokensByUser :many
SELECT id, user_id, name, created_at, last_used_at, expires_at
FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC;

-- name: LookupAPITokenByHash :one
SELECT id, user_id, name, token_hash, created_at, last_used_at, expires_at
FROM api_tokens WHERE token_hash = ?;

-- name: UpdateAPITokenLastUsed :exec
UPDATE api_tokens SET last_used_at = datetime('now') WHERE id = ?;

-- name: DeleteAPIToken :exec
DELETE FROM api_tokens WHERE id = ?;

-- name: DeleteAPITokensForUser :exec
-- Local-cascade when a user is soft-deleted: SQLite's ON DELETE CASCADE
-- on api_tokens.user_id only fires on hard DELETE, not the soft-delete
-- UPDATE that ADR-030's CRDT uses. UserStore.Delete calls this
-- explicitly so a soft-deleted user can't authenticate via leftover
-- tokens.
DELETE FROM api_tokens WHERE user_id = ?;
