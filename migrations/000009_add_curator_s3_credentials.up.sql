-- Curator S3 API (ADR-027): SigV4 access credentials for the read-only
-- S3-compatible object endpoint. Derive-don't-store — the secret_access_key
-- is recomputed via LocalProvider.DeriveKey("vigo-curator-s3-cred-v1", ...) at
-- verify time and never persisted; this row is pure metadata. `scope` is a
-- <publisher>-prefix the credential may read (`*` = every publisher).
CREATE TABLE IF NOT EXISTS curator_s3_credentials (
    access_key_id TEXT PRIMARY KEY,
    scope         TEXT NOT NULL DEFAULT '*',
    name          TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);
