-- name: InsertThrallTraits :one
INSERT INTO thrall_traits (thrall_id, traits)
VALUES (?, ?)
RETURNING id, thrall_id, traits, collected_at;

-- name: GetLatestThrallTraits :one
SELECT id, thrall_id, traits, collected_at
FROM thrall_traits
WHERE thrall_id = ?
ORDER BY collected_at DESC
LIMIT 1;

-- name: PruneThrallTraits :exec
DELETE FROM thrall_traits
WHERE id IN (
  SELECT nt.id FROM thrall_traits nt
  WHERE nt.thrall_id = ?
  ORDER BY nt.collected_at ASC
  LIMIT max(0, (SELECT count(*) FROM thrall_traits nt2 WHERE nt2.thrall_id = ?) - 10)
);
