-- name: InsertNodeNails :one
INSERT INTO node_nails (node_id, nails)
VALUES ($1, $2)
RETURNING id, node_id, nails, collected_at;

-- name: GetLatestNodeNails :one
SELECT id, node_id, nails, collected_at
FROM node_nails
WHERE node_id = $1
ORDER BY collected_at DESC
LIMIT 1;

-- name: PruneNodeNails :exec
DELETE FROM node_nails nn
WHERE nn.node_id = $1
AND nn.id NOT IN (
  SELECT sub.id FROM node_nails sub
  WHERE sub.node_id = nn.node_id
  ORDER BY sub.collected_at DESC
  LIMIT 10
);
