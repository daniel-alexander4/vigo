-- name: InsertNodeTraits :one
INSERT INTO node_traits (node_id, traits)
VALUES ($1, $2)
RETURNING id, node_id, traits, collected_at;

-- name: GetLatestNodeTraits :one
SELECT id, node_id, traits, collected_at
FROM node_traits
WHERE node_id = $1
ORDER BY collected_at DESC
LIMIT 1;

-- name: PruneNodeTraits :exec
DELETE FROM node_traits nn
WHERE nn.node_id = $1
AND nn.id NOT IN (
  SELECT sub.id FROM node_traits sub
  WHERE sub.node_id = nn.node_id
  ORDER BY sub.collected_at DESC
  LIMIT 10
);
