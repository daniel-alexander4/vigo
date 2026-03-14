-- name: InsertConfigPublish :one
INSERT INTO config_publishes (operator, changed_files, removed_files, diff_summary)
VALUES (?, ?, ?, ?)
RETURNING id, operator, published_at, changed_files, removed_files, diff_summary;

-- name: ListConfigPublishes :many
SELECT id, operator, published_at, changed_files, removed_files, diff_summary
FROM config_publishes
ORDER BY published_at DESC
LIMIT ?;

-- name: GetConfigPublish :one
SELECT id, operator, published_at, changed_files, removed_files, diff_summary
FROM config_publishes
WHERE id = ?;
