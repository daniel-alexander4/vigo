-- name: BlockArtifact :exec
INSERT INTO curator_blocked_artifacts (artifact_id, blocked_by, reason)
VALUES (?, ?, ?)
ON CONFLICT(artifact_id) DO UPDATE
  SET blocked_at = datetime('now'), blocked_by = excluded.blocked_by, reason = excluded.reason;

-- name: UnblockArtifact :exec
DELETE FROM curator_blocked_artifacts WHERE artifact_id = ?;

-- name: ListBlockedArtifacts :many
SELECT artifact_id, blocked_at, blocked_by, reason
FROM curator_blocked_artifacts
ORDER BY blocked_at DESC;

-- name: IsArtifactBlocked :one
SELECT COUNT(*) FROM curator_blocked_artifacts WHERE artifact_id = ?;
