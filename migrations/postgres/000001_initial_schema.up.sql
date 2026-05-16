-- Consolidated Postgres schema for Vigo.
-- All tables use TEXT UUIDs with auto-generated v4 defaults via gen_random_uuid().

CREATE TABLE IF NOT EXISTS envoys (
    id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    hostname    TEXT NOT NULL,
    ip_address  TEXT NOT NULL,
    pubkey_pem  TEXT NOT NULL,
    enrolled_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen   TIMESTAMP,
    revoked     INTEGER NOT NULL DEFAULT 0,
    revoked_at  TIMESTAMP,
    force_push  INTEGER NOT NULL DEFAULT 0,
    pending_rotated_secrets TEXT NOT NULL DEFAULT '[]',
    maintenance_until TIMESTAMP,
    agent_version TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_envoys_hostname ON envoys (hostname);

CREATE TABLE IF NOT EXISTS approved_patterns (
    id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    pattern    TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    used_at    TIMESTAMP,
    used_by    TEXT REFERENCES envoys (id)
);

CREATE TABLE IF NOT EXISTS runs (
    id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    envoy_id     TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    started_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at   TIMESTAMP,
    status        TEXT NOT NULL CHECK (status IN ('running', 'success', 'failure', 'partial')),
    module_count  INTEGER NOT NULL DEFAULT 0,
    changed_count INTEGER NOT NULL DEFAULT 0,
    failed_count  INTEGER NOT NULL DEFAULT 0,
    skipped_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_runs_envoy_id ON runs (envoy_id);

CREATE TABLE IF NOT EXISTS run_results (
    id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    run_id         TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    module_name    TEXT NOT NULL,
    resource_type  TEXT NOT NULL,
    resource_name  TEXT NOT NULL,
    action_taken   TEXT NOT NULL,
    changed        INTEGER NOT NULL DEFAULT 0,
    error_message  TEXT,
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_run_results_run_id ON run_results (run_id);

CREATE TABLE IF NOT EXISTS envoy_traits (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    envoy_id    TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    traits       TEXT NOT NULL DEFAULT '{}',
    collected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_envoy_traits_envoy_id_collected ON envoy_traits (envoy_id, collected_at);

CREATE TABLE IF NOT EXISTS plans (
    id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    envoy_id  TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    modules    TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS plan_results (
    id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    plan_id        TEXT NOT NULL REFERENCES plans (id) ON DELETE CASCADE,
    module_name    TEXT NOT NULL,
    resource_type  TEXT NOT NULL,
    resource_name  TEXT NOT NULL,
    planned_action TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS file_snapshots (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    envoy_id    TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    file_path    TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    content      BYTEA,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    run_id       TEXT REFERENCES runs (id)
);

CREATE TABLE IF NOT EXISTS secret_rotation_events (
    id               TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    secret_path      TEXT NOT NULL,
    old_version      TEXT,
    new_version      TEXT,
    rotated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    affected_envoys INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS package_audit (
    id                TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    envoy_id         TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    package_name      TEXT NOT NULL,
    installed_version TEXT,
    desired_version   TEXT,
    detected_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS webhooks (
    id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    url        TEXT NOT NULL,
    secret     TEXT NOT NULL,
    events     TEXT NOT NULL DEFAULT '[]',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    active     INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS convergence_history (
    id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    recorded_at TEXT NOT NULL DEFAULT TO_CHAR(CURRENT_DATE, 'YYYY-MM-DD'),
    total       INTEGER NOT NULL DEFAULT 0,
    converged   INTEGER NOT NULL DEFAULT 0,
    relapsed         INTEGER NOT NULL DEFAULT 0,
    diverged         INTEGER NOT NULL DEFAULT 0,
    failed           INTEGER NOT NULL DEFAULT 0,
    offline     INTEGER NOT NULL DEFAULT 0,
    no_data          INTEGER NOT NULL DEFAULT 0,
    UNIQUE(recorded_at)
);

CREATE TABLE IF NOT EXISTS compliance_history (
    id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    snapshot_date  TEXT NOT NULL DEFAULT TO_CHAR(CURRENT_DATE, 'YYYY-MM-DD'),
    framework      TEXT NOT NULL,
    score_percent  REAL NOT NULL DEFAULT 0,
    satisfied      INTEGER NOT NULL DEFAULT 0,
    partial        INTEGER NOT NULL DEFAULT 0,
    not_satisfied  INTEGER NOT NULL DEFAULT 0,
    total_controls INTEGER NOT NULL DEFAULT 0,
    UNIQUE(snapshot_date, framework)
);

CREATE TABLE IF NOT EXISTS cve_cvss_cache (
    cve_id        TEXT PRIMARY KEY,
    cvss_score    REAL NOT NULL DEFAULT 0.0,
    cvss_vector   TEXT NOT NULL DEFAULT '',
    severity      TEXT NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    cwe           TEXT NOT NULL DEFAULT '',
    refs          TEXT NOT NULL DEFAULT '',
    published_at  TEXT NOT NULL DEFAULT '',
    fetched_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS env_promotions (
    id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    module_name TEXT NOT NULL,
    from_env    TEXT NOT NULL,
    to_env      TEXT NOT NULL,
    diff        TEXT NOT NULL,
    promoted_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- =============================================================================
-- Orchestration tables (push runs, workflows)
-- =============================================================================

CREATE TABLE IF NOT EXISTS workflow_runs (
    id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    workflow_name   TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('pending', 'running', 'complete', 'failed', 'aborted')) DEFAULT 'pending',
    current_step    INTEGER NOT NULL DEFAULT 0,
    total_steps     INTEGER NOT NULL DEFAULT 0,
    operator        TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at      TIMESTAMP,
    finished_at     TIMESTAMP,
    failure_reason  TEXT
);

CREATE INDEX IF NOT EXISTS idx_workflow_runs_status ON workflow_runs (status);

CREATE TABLE IF NOT EXISTS workflow_step_results (
    id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    step_index      INTEGER NOT NULL,
    step_name       TEXT NOT NULL,
    push_run_id     TEXT REFERENCES push_runs (id),
    status          TEXT NOT NULL CHECK (status IN ('pending', 'running', 'complete', 'failed', 'skipped')) DEFAULT 'pending',
    started_at      TIMESTAMP,
    finished_at     TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_workflow_step_results_workflow ON workflow_step_results (workflow_run_id);

-- =============================================================================
-- Users and sessions for web UI / REST API authentication
-- =============================================================================

CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    username        TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL DEFAULT '',
    email           TEXT NOT NULL DEFAULT '',
    role            TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('admin', 'viewer')),
    ssh_public_key  TEXT NOT NULL DEFAULT '',
    oidc_subject    TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT now(),
    updated_at      TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
    token  TEXT PRIMARY KEY,
    data   BYTEA NOT NULL,
    expiry TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions (expiry);

CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT NOT NULL DEFAULT '',
    token_hash   TEXT NOT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT now(),
    last_used_at TIMESTAMP,
    expires_at   TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens (user_id);

-- Config publish audit trail
CREATE TABLE IF NOT EXISTS config_publishes (
    id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    operator      TEXT NOT NULL DEFAULT '',
    published_at  TIMESTAMP NOT NULL DEFAULT now(),
    changed_files TEXT NOT NULL DEFAULT '[]',
    removed_files TEXT NOT NULL DEFAULT '[]',
    diff_summary  TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_config_publishes_published_at ON config_publishes (published_at);
