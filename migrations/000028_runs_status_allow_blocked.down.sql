-- Revert 000028: restore the runs.status CHECK without 'blocked'. Same
-- parent-and-children rebuild as the up migration (see its header for why the
-- children are rebuilt too). Runs recorded as 'blocked' are lossily folded to
-- 'failure' — the old CHECK cannot hold them. Present for migration symmetry.

CREATE TABLE runs_new (
    id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id       TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    started_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    finished_at    DATETIME,
    status         TEXT NOT NULL CHECK (status IN ('running', 'success', 'failure', 'partial')),
    resource_count INTEGER NOT NULL DEFAULT 0,
    changed_count  INTEGER NOT NULL DEFAULT 0,
    failed_count   INTEGER NOT NULL DEFAULT 0,
    skipped_count  INTEGER NOT NULL DEFAULT 0,
    trigger        TEXT NOT NULL DEFAULT 'scheduled' CHECK (trigger IN ('scheduled', 'force-push'))
);
INSERT INTO runs_new (id, envoy_id, started_at, finished_at, status, resource_count, changed_count, failed_count, skipped_count, trigger)
    SELECT id, envoy_id, started_at, finished_at, CASE status WHEN 'blocked' THEN 'failure' ELSE status END, resource_count, changed_count, failed_count, skipped_count, trigger FROM runs;

CREATE TABLE run_results_new (
    id               TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    run_id           TEXT NOT NULL REFERENCES runs_new (id) ON DELETE CASCADE,
    configcrate_name TEXT NOT NULL,
    resource_type    TEXT NOT NULL,
    resource_name    TEXT NOT NULL,
    action_taken     TEXT NOT NULL,
    changed          INTEGER NOT NULL DEFAULT 0,
    error_message    TEXT,
    duration_ms      INTEGER NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO run_results_new (id, run_id, configcrate_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms, created_at)
    SELECT id, run_id, configcrate_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms, created_at FROM run_results;

CREATE TABLE file_snapshots_new (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id     TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    file_path    TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    run_id       TEXT REFERENCES runs_new (id) ON DELETE CASCADE
);
INSERT INTO file_snapshots_new (id, envoy_id, file_path, content_hash, created_at, run_id)
    SELECT id, envoy_id, file_path, content_hash, created_at, run_id FROM file_snapshots;

-- Children first, so the old runs has nothing left to cascade into.
DROP TABLE run_results;
DROP TABLE file_snapshots;
DROP TABLE runs;

ALTER TABLE runs_new RENAME TO runs;
ALTER TABLE run_results_new RENAME TO run_results;
ALTER TABLE file_snapshots_new RENAME TO file_snapshots;

CREATE INDEX IF NOT EXISTS idx_runs_envoy_id ON runs (envoy_id);
CREATE INDEX IF NOT EXISTS idx_runs_envoy_started_status ON runs (envoy_id, started_at, status);
CREATE INDEX IF NOT EXISTS idx_runs_started_at ON runs (started_at DESC);
CREATE INDEX IF NOT EXISTS idx_run_results_run_id ON run_results (run_id);

-- Integrity gate: fail the migration (rolling the transaction back) if the
-- rebuild left a dangling reference or a damaged b-tree. A CHECK on a temp
-- table is the only way plain SQL can abort on a pragma's result.
CREATE TEMP TABLE _m28_down_guard (what TEXT NOT NULL, bad INTEGER NOT NULL CHECK (bad = 0));
INSERT INTO _m28_down_guard SELECT 'foreign_key_check', count(*) FROM pragma_foreign_key_check;
INSERT INTO _m28_down_guard SELECT 'integrity_check', count(*) FROM pragma_integrity_check WHERE integrity_check <> 'ok';
DROP TABLE _m28_down_guard;
