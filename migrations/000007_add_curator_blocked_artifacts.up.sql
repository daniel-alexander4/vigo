-- Curator (ADR-024) operator-moderation backstop: artifact_ids the server
-- refuses to resolve or relay for the fleet, regardless of who published them.
-- The puddle-signed catalog entry still exists and gossips, and the DR bytes
-- still sit on target-matched envoys — only a publisher `rm` truly purges.
CREATE TABLE IF NOT EXISTS curator_blocked_artifacts (
    artifact_id TEXT PRIMARY KEY,
    blocked_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    blocked_by  TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT ''
);
