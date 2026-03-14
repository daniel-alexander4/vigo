-- name: InsertAPIToken :one
INSERT INTO api_tokens (user_id, name, token_hash, expires_at)
VALUES (?, ?, ?, ?)
RETURNING id, user_id, name, token_hash, created_at, last_used_at, expires_at;

-- name: GetAPITokenByID :one
SELECT id, user_id, name, token_hash, created_at, last_used_at, expires_at
FROM api_tokens WHERE id = ?;

-- name: ListAPITokensByUser :many
SELECT id, user_id, name, created_at, last_used_at, expires_at
FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC;

-- name: ListAllAPITokens :many
SELECT t.id, t.user_id, t.name, t.token_hash, t.created_at, t.last_used_at, t.expires_at
FROM api_tokens t ORDER BY t.created_at DESC;

-- name: UpdateAPITokenLastUsed :exec
UPDATE api_tokens SET last_used_at = datetime('now') WHERE id = ?;

-- name: DeleteAPIToken :exec
DELETE FROM api_tokens WHERE id = ?;

-- name: DeleteExpiredAPITokens :exec
DELETE FROM api_tokens WHERE expires_at IS NOT NULL AND expires_at < datetime('now');
