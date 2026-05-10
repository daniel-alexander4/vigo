-- Consolidated SQLite schema for Vigo.
-- All tables use TEXT UUIDs with auto-generated v4 defaults.

CREATE TABLE IF NOT EXISTS envoys (
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
    maintenance_until DATETIME,
    agent_version TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_envoys_hostname ON envoys (hostname);

CREATE TABLE IF NOT EXISTS approved_patterns (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    pattern    TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    used_at    DATETIME,
    used_by    TEXT REFERENCES envoys (id)
);

CREATE TABLE IF NOT EXISTS runs (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id     TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    started_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    finished_at   DATETIME,
    status        TEXT NOT NULL CHECK (status IN ('running', 'success', 'failure', 'partial')),
    module_count  INTEGER NOT NULL DEFAULT 0,
    changed_count INTEGER NOT NULL DEFAULT 0,
    failed_count  INTEGER NOT NULL DEFAULT 0,
    skipped_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_runs_envoy_id ON runs (envoy_id);

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

CREATE TABLE IF NOT EXISTS envoy_traits (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id    TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    traits       TEXT NOT NULL DEFAULT '{}',
    collected_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_envoy_traits_envoy_id_collected ON envoy_traits (envoy_id, collected_at);

CREATE TABLE IF NOT EXISTS plans (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id  TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
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
    envoy_id    TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
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
    affected_envoys INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS package_audit (
    id                TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id         TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
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

CREATE TABLE IF NOT EXISTS convergence_history (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    recorded_at TEXT NOT NULL DEFAULT (date('now')),
    total       INTEGER NOT NULL DEFAULT 0,
    -- Failure axis (mutually exclusive; sums to total).
    converged   INTEGER NOT NULL DEFAULT 0,
    degraded    INTEGER NOT NULL DEFAULT 0,
    failed      INTEGER NOT NULL DEFAULT 0,
    no_data     INTEGER NOT NULL DEFAULT 0,
    -- Drift axis (overlapping; diverged ⊆ changed).
    changed     INTEGER NOT NULL DEFAULT 0,
    diverged    INTEGER NOT NULL DEFAULT 0,
    -- Reachability axis.
    offline     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(recorded_at)
);

CREATE TABLE IF NOT EXISTS compliance_history (
    id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    snapshot_date  TEXT NOT NULL DEFAULT (date('now')),
    framework      TEXT NOT NULL,
    score_percent  REAL NOT NULL DEFAULT 0,
    satisfied      INTEGER NOT NULL DEFAULT 0,
    partial        INTEGER NOT NULL DEFAULT 0,
    not_satisfied  INTEGER NOT NULL DEFAULT 0,
    total_controls INTEGER NOT NULL DEFAULT 0,
    UNIQUE(snapshot_date, framework)
);

CREATE TABLE IF NOT EXISTS security_posture_history (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    snapshot_date TEXT NOT NULL DEFAULT (date('now')),
    cve_critical  INTEGER NOT NULL DEFAULT 0,
    cve_high      INTEGER NOT NULL DEFAULT 0,
    cve_medium    INTEGER NOT NULL DEFAULT 0,
    cve_low       INTEGER NOT NULL DEFAULT 0,
    avg_hardening INTEGER NOT NULL DEFAULT 0,
    integrity_incidents INTEGER NOT NULL DEFAULT 0,
    rootkit_warnings    INTEGER NOT NULL DEFAULT 0,
    malware_detections  INTEGER NOT NULL DEFAULT 0,
    scanned_hosts INTEGER NOT NULL DEFAULT 0,
    total_hosts   INTEGER NOT NULL DEFAULT 0,
    UNIQUE(snapshot_date)
);

CREATE TABLE IF NOT EXISTS risk_posture_history (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    snapshot_date TEXT NOT NULL DEFAULT (date('now')),
    avg_score     INTEGER NOT NULL DEFAULT 0,
    max_score     INTEGER NOT NULL DEFAULT 0,
    dist_low      INTEGER NOT NULL DEFAULT 0,
    dist_medium   INTEGER NOT NULL DEFAULT 0,
    dist_high     INTEGER NOT NULL DEFAULT 0,
    dist_critical INTEGER NOT NULL DEFAULT 0,
    scored_envoys INTEGER NOT NULL DEFAULT 0,
    total_envoys  INTEGER NOT NULL DEFAULT 0,
    UNIQUE(snapshot_date)
);

CREATE TABLE IF NOT EXISTS cve_cvss_cache (
    cve_id      TEXT PRIMARY KEY,
    cvss_score  REAL NOT NULL DEFAULT 0.0,
    cvss_vector TEXT NOT NULL DEFAULT '',
    severity    TEXT NOT NULL DEFAULT '',
    fetched_at  DATETIME NOT NULL DEFAULT (datetime('now'))
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
    envoy_id    TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
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
CREATE INDEX IF NOT EXISTS idx_push_run_targets_envoy_id ON push_run_targets (envoy_id);

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
    envoy_id    TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
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
CREATE INDEX IF NOT EXISTS idx_task_run_targets_envoy_id ON task_run_targets (envoy_id);

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
    role            TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('admin', 'viewer', 'compliance')),
    permissions     TEXT NOT NULL DEFAULT '',
    ssh_public_key  TEXT NOT NULL DEFAULT '',
    scrier_username TEXT NOT NULL DEFAULT '',
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

CREATE TABLE IF NOT EXISTS audit_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    prev_hash   TEXT NOT NULL DEFAULT '',
    hash        TEXT NOT NULL,
    timestamp   DATETIME NOT NULL DEFAULT (datetime('now')),
    event_type  TEXT NOT NULL,
    actor       TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    summary     TEXT NOT NULL DEFAULT '',
    payload     TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_audit_entries_timestamp ON audit_entries (timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_entries_event_type ON audit_entries (event_type);

CREATE TABLE IF NOT EXISTS mttr_stats (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    recorded_at     TEXT NOT NULL DEFAULT (date('now')),
    total_incidents INTEGER NOT NULL DEFAULT 0,
    resolved        INTEGER NOT NULL DEFAULT 0,
    mttr_seconds    INTEGER NOT NULL DEFAULT 0,
    p50_seconds     INTEGER NOT NULL DEFAULT 0,
    p95_seconds     INTEGER NOT NULL DEFAULT 0,
    open_incidents  INTEGER NOT NULL DEFAULT 0,
    UNIQUE(recorded_at)
);

CREATE INDEX IF NOT EXISTS idx_runs_envoy_started_status ON runs (envoy_id, started_at, status);
-- Recent-activity queries (dashboard, /runs) order by started_at globally.
-- The composite index above leads with envoy_id so it can't satisfy
-- ORDER BY started_at without a temp B-tree sort over the entire table.
CREATE INDEX IF NOT EXISTS idx_runs_started_at ON runs (started_at DESC);

CREATE TABLE IF NOT EXISTS compliance_waivers (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    framework   TEXT NOT NULL,
    control_id  TEXT NOT NULL,
    reason      TEXT NOT NULL,
    approver    TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at  DATETIME,
    active      INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_compliance_waivers_framework ON compliance_waivers (framework, control_id);
CREATE INDEX IF NOT EXISTS idx_compliance_waivers_active ON compliance_waivers (active);
