-- Users and sessions for web UI / REST API authentication.

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
