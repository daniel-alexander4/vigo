-- 000027: drop file_snapshots.content.
--
-- The agent no longer ships prior file content to the server: it was a
-- plaintext-secret leak (a content-managed TLS key / .env / htpasswd sent its
-- old contents into the gRPC ReportResult payload and this column on every
-- change, violating "secrets never appear in plaintext in the DB or gRPC
-- payloads"), and nothing ever read it back — Rollback only checks that a
-- snapshot row exists for the path, then force-pushes the desired policy.
--
-- Rebuilding without the column also purges any plaintext already captured
-- into the table. The row still records path + content_hash + run_id, which is
-- all the Rollback existence check needs. SQLite can't drop a column with
-- foreign keys in place, so rebuild (create/copy/drop/rename, mirroring 000019
-- and carrying its ON DELETE CASCADE foreign keys forward).

CREATE TABLE file_snapshots_new (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id     TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    file_path    TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    run_id       TEXT REFERENCES runs (id) ON DELETE CASCADE
);
INSERT INTO file_snapshots_new (id, envoy_id, file_path, content_hash, created_at, run_id)
    SELECT id, envoy_id, file_path, content_hash, created_at, run_id FROM file_snapshots;
DROP TABLE file_snapshots;
ALTER TABLE file_snapshots_new RENAME TO file_snapshots;
