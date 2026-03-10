-- name: InsertNodeTraits :one
INSERT INTO node_traits (node_id, traits)
VALUES (?, ?)
RETURNING id, node_id, traits, collected_at;

-- name: GetLatestNodeTraits :one
SELECT id, node_id, traits, collected_at
FROM node_traits
WHERE node_id = ?
ORDER BY collected_at DESC
LIMIT 1;

-- name: PruneNodeTraits :exec
DELETE FROM node_traits
WHERE id IN (
  SELECT nt.id FROM node_traits nt
  WHERE nt.node_id = ?
  ORDER BY nt.collected_at ASC
  LIMIT max(0, (SELECT count(*) FROM node_traits nt2 WHERE nt2.node_id = ?) - 10)
);
