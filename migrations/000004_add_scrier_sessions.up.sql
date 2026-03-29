-- Scrier remote access session tracking.
CREATE TABLE IF NOT EXISTS scrier_sessions (
    id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id       TEXT NOT NULL REFERENCES envoys(id) ON DELETE CASCADE,
    user_id        TEXT NOT NULL DEFAULT '',
    username       TEXT NOT NULL DEFAULT '',
    protocol       TEXT NOT NULL DEFAULT 'ssh',
    target_port    INTEGER NOT NULL DEFAULT 22,
    tunnel_id      TEXT NOT NULL DEFAULT '',
    started_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    ended_at       DATETIME,
    duration_ms    INTEGER,
    status         TEXT NOT NULL DEFAULT 'active',
    recording_path TEXT NOT NULL DEFAULT '',
    error_message  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_scrier_sessions_envoy ON scrier_sessions(envoy_id);
CREATE INDEX IF NOT EXISTS idx_scrier_sessions_user ON scrier_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_scrier_sessions_status ON scrier_sessions(status);
