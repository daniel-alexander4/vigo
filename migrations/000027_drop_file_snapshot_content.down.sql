-- Revert 000027: restore the file_snapshots.content column (nullable BLOB).
-- Existing rows get NULL content — the purged plaintext is not recoverable
-- (by design). Present only for migration symmetry.

CREATE TABLE file_snapshots_old (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id     TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    file_path    TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    content      BLOB,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    run_id       TEXT REFERENCES runs (id) ON DELETE CASCADE
);
INSERT INTO file_snapshots_old (id, envoy_id, file_path, content_hash, created_at, run_id)
    SELECT id, envoy_id, file_path, content_hash, created_at, run_id FROM file_snapshots;
DROP TABLE file_snapshots;
ALTER TABLE file_snapshots_old RENAME TO file_snapshots;
