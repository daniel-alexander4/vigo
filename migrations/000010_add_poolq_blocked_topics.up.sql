-- Poolq (ADR-029) operator-moderation backstop: topic_ids the server
-- refuses to serve the message log for, regardless of who founded the
-- topic. The puddle-signed messages still exist in agents' per-user
-- state and the trait collector still emits them — only the server's
-- range/log endpoints refuse, fleet-wide. Same shape as the curator
-- blocked-artifacts table (migration 000007).
CREATE TABLE IF NOT EXISTS poolq_blocked_topics (
    topic_id    TEXT PRIMARY KEY,
    blocked_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    blocked_by  TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT ''
);
