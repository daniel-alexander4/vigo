-- Consolidated SQLite schema for Hobnob.
-- All tables use TEXT UUIDs with auto-generated v4 defaults.

CREATE TABLE IF NOT EXISTS thralls (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    hostname    TEXT NOT NULL,
    ip_address  TEXT NOT NULL,
    pubkey_pem  TEXT NOT NULL,
    enrolled_at DATETIME NOT NULL DEFAULT (datetime('now')),
    last_seen   DATETIME,
    revoked     INTEGER NOT NULL DEFAULT 0,
    revoked_at  DATETIME,
    force_push  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_thralls_hostname ON thralls (hostname);

CREATE TABLE IF NOT EXISTS approved_patterns (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    pattern    TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    used_at    DATETIME,
    used_by    TEXT REFERENCES thralls (id)
);

CREATE TABLE IF NOT EXISTS runs (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    thrall_id     TEXT NOT NULL REFERENCES thralls (id) ON DELETE CASCADE,
    started_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    finished_at   DATETIME,
    status        TEXT NOT NULL CHECK (status IN ('running', 'success', 'failure', 'partial')),
    module_count  INTEGER NOT NULL DEFAULT 0,
    changed_count INTEGER NOT NULL DEFAULT 0,
    failed_count  INTEGER NOT NULL DEFAULT 0,
    skipped_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_runs_thrall_id ON runs (thrall_id);

CREATE TABLE IF NOT EXISTS run_results (
    id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    run_id         TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    module_name    TEXT NOT NULL,
    resource_type  TEXT NOT NULL,
    resource_name  TEXT NOT NULL,
    action_taken   TEXT NOT NULL,
    changed        INTEGER NOT NULL DEFAULT 0,
    error_message  TEXT,
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    created_at     DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_run_results_run_id ON run_results (run_id);

CREATE TABLE IF NOT EXISTS thrall_traits (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    thrall_id    TEXT NOT NULL REFERENCES thralls (id) ON DELETE CASCADE,
    traits       TEXT NOT NULL DEFAULT '{}',
    collected_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_thrall_traits_thrall_id_collected ON thrall_traits (thrall_id, collected_at);

CREATE TABLE IF NOT EXISTS plans (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    thrall_id  TEXT NOT NULL REFERENCES thralls (id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    modules    TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS plan_results (
    id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    plan_id        TEXT NOT NULL REFERENCES plans (id) ON DELETE CASCADE,
    module_name    TEXT NOT NULL,
    resource_type  TEXT NOT NULL,
    resource_name  TEXT NOT NULL,
    planned_action TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS file_snapshots (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    thrall_id    TEXT NOT NULL REFERENCES thralls (id) ON DELETE CASCADE,
    file_path    TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    content      BLOB,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    run_id       TEXT REFERENCES runs (id)
);

CREATE TABLE IF NOT EXISTS secret_rotation_events (
    id               TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    secret_path      TEXT NOT NULL,
    old_version      TEXT,
    new_version      TEXT,
    rotated_at       DATETIME NOT NULL DEFAULT (datetime('now')),
    affected_thralls INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS package_audit (
    id                TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    thrall_id         TEXT NOT NULL REFERENCES thralls (id) ON DELETE CASCADE,
    package_name      TEXT NOT NULL,
    installed_version TEXT,
    desired_version   TEXT,
    detected_at       DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS webhooks (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    url        TEXT NOT NULL,
    secret     TEXT NOT NULL,
    events     TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    active     INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS compliance_history (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    recorded_at TEXT NOT NULL DEFAULT (date('now')),
    total       INTEGER NOT NULL DEFAULT 0,
    compliant   INTEGER NOT NULL DEFAULT 0,
    drifted     INTEGER NOT NULL DEFAULT 0,
    failing     INTEGER NOT NULL DEFAULT 0,
    stale       INTEGER NOT NULL DEFAULT 0,
    unknown     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(recorded_at)
);

CREATE TABLE IF NOT EXISTS env_promotions (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    module_name TEXT NOT NULL,
    from_env    TEXT NOT NULL,
    to_env      TEXT NOT NULL,
    diff        TEXT NOT NULL,
    promoted_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
