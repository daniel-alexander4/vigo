-- name: CreateApprovedPattern :one
INSERT INTO approved_patterns (pattern, token_hash)
VALUES (?, ?)
RETURNING id, pattern, token_hash, created_at, used_at, used_by;

-- name: GetApprovedPatternsByPattern :many
SELECT id, pattern, token_hash, created_at, used_at, used_by
FROM approved_patterns
WHERE pattern = ? AND used_at IS NULL;

-- name: MarkPatternUsed :exec
UPDATE approved_patterns
SET used_at = datetime('now'), used_by = ?2
WHERE id = ?1;

-- name: ListApprovedPatterns :many
SELECT id, pattern, token_hash, created_at, used_at, used_by
FROM approved_patterns
ORDER BY created_at DESC;

-- name: DeleteApprovedPattern :exec
DELETE FROM approved_patterns
WHERE id = ?;

-- name: ListUnusedApprovedPatterns :many
SELECT id, pattern, token_hash, created_at, used_at, used_by
FROM approved_patterns
WHERE used_at IS NULL
ORDER BY created_at DESC;
