-- name: InsertScrierSession :one
INSERT INTO scrier_sessions (envoy_id, user_id, username, protocol, target_port, tunnel_id, status)
VALUES (?, ?, ?, ?, ?, ?, 'active')
RETURNING id, envoy_id, user_id, username, protocol, target_port, tunnel_id, started_at, ended_at, duration_ms, status, recording_path, error_message;

-- name: EndScrierSession :exec
UPDATE scrier_sessions
SET ended_at = datetime('now'),
    duration_ms = CAST((julianday('now') - julianday(started_at)) * 86400000 AS INTEGER),
    status = ?
WHERE tunnel_id = ? AND status = 'active';

-- name: EndScrierSessionWithError :exec
UPDATE scrier_sessions
SET ended_at = datetime('now'),
    duration_ms = CAST((julianday('now') - julianday(started_at)) * 86400000 AS INTEGER),
    status = 'error',
    error_message = ?
WHERE tunnel_id = ? AND status = 'active';

-- name: CountActiveScrierSessions :one
SELECT COUNT(*) FROM scrier_sessions WHERE status = 'active';

-- name: CountActiveScrierSessionsForEnvoy :one
SELECT COUNT(*) FROM scrier_sessions WHERE envoy_id = ? AND status = 'active';

-- name: ListScrierSessionsByEnvoy :many
SELECT s.id, s.envoy_id, s.user_id, s.username, s.protocol, s.target_port,
       s.tunnel_id, s.started_at, s.ended_at, s.duration_ms, s.status,
       s.recording_path, s.error_message
FROM scrier_sessions s
WHERE s.envoy_id = ?
ORDER BY s.started_at DESC
LIMIT 50;

-- name: ListRecentScrierSessions :many
SELECT s.id, s.envoy_id, e.hostname, s.username, s.protocol, s.target_port,
       s.started_at, s.ended_at, s.duration_ms, s.status
FROM scrier_sessions s
JOIN envoys e ON s.envoy_id = e.id
ORDER BY s.started_at DESC
LIMIT ?;
