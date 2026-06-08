-- Recreate package_audit (mirrors 000001_initial_schema).
CREATE TABLE IF NOT EXISTS package_audit (
    id                TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id         TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    package_name      TEXT NOT NULL,
    installed_version TEXT,
    desired_version   TEXT,
    detected_at       DATETIME NOT NULL DEFAULT (datetime('now'))
);
