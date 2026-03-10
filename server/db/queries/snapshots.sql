-- name: InsertFileSnapshot :one
INSERT INTO file_snapshots (node_id, file_path, content_hash, content, run_id)
VALUES (?, ?, ?, ?, ?)
RETURNING id, node_id, file_path, content_hash, content, created_at, run_id;

-- name: ListFileSnapshots :many
SELECT id, node_id, file_path, content_hash, content, created_at, run_id
FROM file_snapshots
WHERE node_id = ?1 AND file_path = ?2
ORDER BY created_at DESC;
