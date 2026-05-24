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
