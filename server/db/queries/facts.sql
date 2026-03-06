-- name: InsertClientFacts :one
INSERT INTO client_facts (client_id, facts)
VALUES ($1, $2)
RETURNING id, client_id, facts, collected_at;

-- name: GetLatestClientFacts :one
SELECT id, client_id, facts, collected_at
FROM client_facts
WHERE client_id = $1
ORDER BY collected_at DESC
LIMIT 1;

-- name: PruneClientFacts :exec
DELETE FROM client_facts cf
WHERE cf.client_id = $1
AND cf.id NOT IN (
  SELECT sub.id FROM client_facts sub
  WHERE sub.client_id = cf.client_id
  ORDER BY sub.collected_at DESC
  LIMIT 10
);
