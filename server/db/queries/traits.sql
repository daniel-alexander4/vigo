-- name: InsertEnvoyTraits :one
INSERT INTO envoy_traits (envoy_id, traits)
VALUES (?, ?)
RETURNING id, envoy_id, traits, collected_at;

-- name: GetLatestEnvoyTraits :one
SELECT id, envoy_id, traits, collected_at
FROM envoy_traits
WHERE envoy_id = ?
ORDER BY collected_at DESC
LIMIT 1;

-- name: PruneEnvoyTraits :exec
DELETE FROM envoy_traits
WHERE id IN (
  SELECT nt.id FROM envoy_traits nt
  WHERE nt.envoy_id = ?
  ORDER BY nt.collected_at ASC
  LIMIT max(0, (SELECT count(*) FROM envoy_traits nt2 WHERE nt2.envoy_id = ?) - 10)
);
