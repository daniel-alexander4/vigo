-- name: ListFileSnapshots :many
SELECT id, envoy_id, file_path, content_hash, created_at, run_id
FROM file_snapshots
WHERE envoy_id = ?1 AND file_path = ?2
ORDER BY created_at DESC;

-- name: InsertFileSnapshot :exec
INSERT INTO file_snapshots (envoy_id, file_path, content_hash, run_id)
VALUES (?1, ?2, ?3, ?4);
