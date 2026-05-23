-- name: BlockPoolqTopic :exec
INSERT INTO poolq_blocked_topics (topic_id, blocked_by, reason)
VALUES (?, ?, ?)
ON CONFLICT(topic_id) DO UPDATE
  SET blocked_at = datetime('now'), blocked_by = excluded.blocked_by, reason = excluded.reason;

-- name: UnblockPoolqTopic :exec
DELETE FROM poolq_blocked_topics WHERE topic_id = ?;

-- name: ListBlockedPoolqTopics :many
SELECT topic_id, blocked_at, blocked_by, reason
FROM poolq_blocked_topics
ORDER BY blocked_at DESC;

-- name: IsPoolqTopicBlocked :one
SELECT COUNT(*) FROM poolq_blocked_topics WHERE topic_id = ?;
