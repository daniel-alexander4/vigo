-- name: UpsertRiskPostureSnapshot :exec
INSERT INTO risk_posture_history (
    avg_score, max_score, dist_low, dist_medium, dist_high, dist_critical,
    scored_envoys, total_envoys
) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)
ON CONFLICT(snapshot_date) DO UPDATE SET
    avg_score     = excluded.avg_score,
    max_score     = excluded.max_score,
    dist_low      = excluded.dist_low,
    dist_medium   = excluded.dist_medium,
    dist_high     = excluded.dist_high,
    dist_critical = excluded.dist_critical,
    scored_envoys = excluded.scored_envoys,
    total_envoys  = excluded.total_envoys;

-- name: ListRiskPostureHistory :many
SELECT snapshot_date, avg_score, max_score, dist_low, dist_medium, dist_high,
       dist_critical, scored_envoys, total_envoys
FROM risk_posture_history
ORDER BY snapshot_date DESC
LIMIT ?;
