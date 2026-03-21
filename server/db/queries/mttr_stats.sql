-- name: UpsertMTTRStats :exec
INSERT INTO mttr_stats (recorded_at, total_incidents, resolved, mttr_seconds, p50_seconds, p95_seconds, open_incidents)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)
ON CONFLICT(recorded_at) DO UPDATE SET
  total_incidents = excluded.total_incidents,
  resolved        = excluded.resolved,
  mttr_seconds    = excluded.mttr_seconds,
  p50_seconds     = excluded.p50_seconds,
  p95_seconds     = excluded.p95_seconds,
  open_incidents  = excluded.open_incidents;

-- name: ListMTTRStats :many
SELECT id, recorded_at, total_incidents, resolved, mttr_seconds, p50_seconds, p95_seconds, open_incidents
FROM mttr_stats
WHERE recorded_at BETWEEN ?1 AND ?2
ORDER BY recorded_at DESC;

-- name: GetLatestMTTRStats :one
SELECT id, recorded_at, total_incidents, resolved, mttr_seconds, p50_seconds, p95_seconds, open_incidents
FROM mttr_stats
ORDER BY recorded_at DESC
LIMIT 1;
