-- name: InsertFileSnapshot :one
INSERT INTO file_snapshots (client_id, file_path, content_hash, content, run_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, client_id, file_path, content_hash, content, created_at, run_id;

-- name: ListFileSnapshots :many
SELECT id, client_id, file_path, content_hash, content, created_at, run_id
FROM file_snapshots
WHERE client_id = $1 AND file_path = $2
ORDER BY created_at DESC;
