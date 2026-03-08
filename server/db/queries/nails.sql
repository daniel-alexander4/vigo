-- name: InsertClientNails :one
INSERT INTO client_nails (client_id, nails)
VALUES ($1, $2)
RETURNING id, client_id, nails, collected_at;

-- name: GetLatestClientNails :one
SELECT id, client_id, nails, collected_at
FROM client_nails
WHERE client_id = $1
ORDER BY collected_at DESC
LIMIT 1;

-- name: PruneClientNails :exec
DELETE FROM client_nails cf
WHERE cf.client_id = $1
AND cf.id NOT IN (
  SELECT sub.id FROM client_nails sub
  WHERE sub.client_id = cf.client_id
  ORDER BY sub.collected_at DESC
  LIMIT 10
);
