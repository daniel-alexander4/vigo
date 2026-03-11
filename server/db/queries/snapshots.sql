-- name: InsertFileSnapshot :one
INSERT INTO file_snapshots (thrall_id, file_path, content_hash, content, run_id)
VALUES (?, ?, ?, ?, ?)
RETURNING id, thrall_id, file_path, content_hash, content, created_at, run_id;

-- name: ListFileSnapshots :many
SELECT id, thrall_id, file_path, content_hash, content, created_at, run_id
FROM file_snapshots
WHERE thrall_id = ?1 AND file_path = ?2
ORDER BY created_at DESC;
