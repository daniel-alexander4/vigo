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
    force_push  INTEGER NOT NULL DEFAULT 0,
    pending_rotated_secrets TEXT NOT NULL DEFAULT '[]',
    tags        TEXT NOT NULL DEFAULT '',
    maintenance_until DATETIME
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
    recurring_drift  INTEGER NOT NULL DEFAULT 0,
    errors           INTEGER NOT NULL DEFAULT 0,
    lost_contact     INTEGER NOT NULL DEFAULT 0,
    no_data          INTEGER NOT NULL DEFAULT 0,
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

-- =============================================================================
-- Orchestration tables (push runs, workflows)
-- =============================================================================

CREATE TABLE IF NOT EXISTS push_runs (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    command       TEXT NOT NULL,
    target_glob   TEXT NOT NULL,
    operator      TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL CHECK (status IN ('pending', 'running', 'complete', 'failed', 'cancelled')) DEFAULT 'pending',
    total_targets INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    started_at    DATETIME,
    finished_at   DATETIME,
    timeout_secs  INTEGER NOT NULL DEFAULT 60,
    batch_size    TEXT,
    max_failures  TEXT
);

CREATE INDEX IF NOT EXISTS idx_push_runs_status ON push_runs (status);
CREATE INDEX IF NOT EXISTS idx_push_runs_created_at ON push_runs (created_at);

CREATE TABLE IF NOT EXISTS push_run_targets (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    push_run_id  TEXT NOT NULL REFERENCES push_runs (id) ON DELETE CASCADE,
    thrall_id    TEXT NOT NULL REFERENCES thralls (id) ON DELETE CASCADE,
    hostname     TEXT NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('pending', 'dispatched', 'complete', 'failed', 'timeout', 'offline', 'rejected', 'skipped')) DEFAULT 'pending',
    exit_code    INTEGER,
    stdout       TEXT,
    stderr       TEXT,
    duration_ms  INTEGER,
    error_msg    TEXT,
    dispatched_at DATETIME,
    finished_at  DATETIME
);

CREATE INDEX IF NOT EXISTS idx_push_run_targets_push_run_id ON push_run_targets (push_run_id);
CREATE INDEX IF NOT EXISTS idx_push_run_targets_thrall_id ON push_run_targets (thrall_id);

CREATE TABLE IF NOT EXISTS task_runs (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    command       TEXT NOT NULL,
    target_glob   TEXT NOT NULL,
    operator      TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL CHECK (status IN ('pending', 'running', 'complete', 'failed', 'cancelled')) DEFAULT 'pending',
    total_targets INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    started_at    DATETIME,
    finished_at   DATETIME,
    timeout_secs  INTEGER NOT NULL DEFAULT 60,
    batch_size    TEXT,
    max_failures  TEXT
);

CREATE INDEX IF NOT EXISTS idx_task_runs_status ON task_runs (status);
CREATE INDEX IF NOT EXISTS idx_task_runs_created_at ON task_runs (created_at);

CREATE TABLE IF NOT EXISTS task_run_targets (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    task_run_id  TEXT NOT NULL REFERENCES task_runs (id) ON DELETE CASCADE,
    thrall_id    TEXT NOT NULL REFERENCES thralls (id) ON DELETE CASCADE,
    hostname     TEXT NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('pending', 'dispatched', 'complete', 'failed', 'timeout', 'offline', 'rejected', 'skipped')) DEFAULT 'pending',
    exit_code    INTEGER,
    stdout       TEXT,
    stderr       TEXT,
    duration_ms  INTEGER,
    error_msg    TEXT,
    dispatched_at DATETIME,
    finished_at  DATETIME
);

CREATE INDEX IF NOT EXISTS idx_task_run_targets_task_run_id ON task_run_targets (task_run_id);
CREATE INDEX IF NOT EXISTS idx_task_run_targets_thrall_id ON task_run_targets (thrall_id);

CREATE TABLE IF NOT EXISTS workflow_runs (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    workflow_name   TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('pending', 'running', 'complete', 'failed', 'aborted')) DEFAULT 'pending',
    current_step    INTEGER NOT NULL DEFAULT 0,
    total_steps     INTEGER NOT NULL DEFAULT 0,
    operator        TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    started_at      DATETIME,
    finished_at     DATETIME,
    failure_reason  TEXT
);

CREATE INDEX IF NOT EXISTS idx_workflow_runs_status ON workflow_runs (status);

CREATE TABLE IF NOT EXISTS workflow_step_results (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    step_index      INTEGER NOT NULL,
    step_name       TEXT NOT NULL,
    push_run_id     TEXT REFERENCES push_runs (id),
    status          TEXT NOT NULL CHECK (status IN ('pending', 'running', 'complete', 'failed', 'skipped')) DEFAULT 'pending',
    started_at      DATETIME,
    finished_at     DATETIME
);

CREATE INDEX IF NOT EXISTS idx_workflow_step_results_workflow ON workflow_step_results (workflow_run_id);

-- =============================================================================
-- Users and sessions for web UI / REST API authentication
-- =============================================================================

CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    username        TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL DEFAULT '',
    email           TEXT NOT NULL DEFAULT '',
    role            TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('admin', 'viewer')),
    permissions     TEXT NOT NULL DEFAULT '',
    ssh_public_key  TEXT NOT NULL DEFAULT '',
    oidc_subject    TEXT NOT NULL DEFAULT '',
    totp_enabled    INTEGER NOT NULL DEFAULT 0,
    totp_recovery   TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sessions (
    token  TEXT PRIMARY KEY,
    data   BLOB NOT NULL,
    expiry REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions (expiry);

CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT NOT NULL DEFAULT '',
    token_hash   TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    last_used_at DATETIME,
    expires_at   DATETIME
);

CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens (user_id);

-- =============================================================================
-- Config publish audit trail
-- =============================================================================

CREATE TABLE IF NOT EXISTS config_publishes (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    operator      TEXT NOT NULL DEFAULT '',
    published_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    changed_files TEXT NOT NULL DEFAULT '[]',
    removed_files TEXT NOT NULL DEFAULT '[]',
    diff_summary  TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_config_publishes_published_at ON config_publishes (published_at);

CREATE TABLE IF NOT EXISTS config_changes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'expired')),
    requested_by  TEXT NOT NULL,
    approved_by   TEXT,
    requested_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    resolved_at   DATETIME,
    expires_at    DATETIME NOT NULL,
    diff_summary  TEXT NOT NULL DEFAULT '',
    reject_reason TEXT
);

CREATE INDEX IF NOT EXISTS idx_config_changes_status ON config_changes (status);

CREATE TABLE IF NOT EXISTS audit_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    prev_hash   TEXT NOT NULL DEFAULT '',
    hash        TEXT NOT NULL,
    timestamp   DATETIME NOT NULL DEFAULT (datetime('now')),
    event_type  TEXT NOT NULL,
    actor       TEXT NOT NULL DEFAULT '',
    summary     TEXT NOT NULL DEFAULT '',
    payload     TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_audit_entries_timestamp ON audit_entries (timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_entries_event_type ON audit_entries (event_type);
