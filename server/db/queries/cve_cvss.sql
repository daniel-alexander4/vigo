-- name: UpsertCVECVSS :exec
INSERT INTO cve_cvss_cache (cve_id, cvss_score, cvss_vector, severity)
VALUES (?1, ?2, ?3, ?4)
ON CONFLICT(cve_id) DO UPDATE SET
    cvss_score  = excluded.cvss_score,
    cvss_vector = excluded.cvss_vector,
    severity    = excluded.severity,
    fetched_at  = datetime('now');

-- name: GetCVECVSS :one
SELECT cve_id, cvss_score, cvss_vector, severity, fetched_at
FROM cve_cvss_cache
WHERE cve_id = ?;

-- name: ListCachedCVEs :many
SELECT cve_id, cvss_score, severity, fetched_at
FROM cve_cvss_cache
ORDER BY cvss_score DESC;
