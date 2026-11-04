-- 000028: allow runs.status = 'blocked'.
--
-- summarizeResults (server/grpc/summarize.go) marks a run 'blocked' when a
-- guardrail blocked any resource, but the 000001 CHECK only admitted
-- running/success/failure/partial. FinishRun therefore failed for every such
-- run: the unary RecordRun transaction rolled back (ReportResult → Internal)
-- and the delta path left a permanent 'running' row that retention never prunes.
--
-- SQLite can't alter a CHECK in place, so rebuild runs. runs is a PARENT:
-- run_results.run_id and file_snapshots.run_id reference it ON DELETE CASCADE.
-- With foreign keys on (the server DSN sets _foreign_keys=on, and PRAGMA
-- foreign_keys is a no-op inside the migration transaction), DROP TABLE runs
-- runs an implicit DELETE FROM runs that would cascade-delete every child row.
-- So both children are rebuilt too, pointing at runs_new; the old children are
-- dropped first, then the old runs (which by then has no children), and the
-- final RENAME of runs_new → runs rewrites the children's REFERENCES text back
-- to "runs". Column order, defaults, CHECKs, FKs and indexes are carried over
-- exactly; 'partial' stays (the /runs UI still filters on it).

CREATE TABLE runs_new (
    id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id       TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    started_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    finished_at    DATETIME,
    status         TEXT NOT NULL CHECK (status IN ('running', 'success', 'failure', 'partial', 'blocked')),
    resource_count INTEGER NOT NULL DEFAULT 0,
    changed_count  INTEGER NOT NULL DEFAULT 0,
    failed_count   INTEGER NOT NULL DEFAULT 0,
    skipped_count  INTEGER NOT NULL DEFAULT 0,
    trigger        TEXT NOT NULL DEFAULT 'scheduled' CHECK (trigger IN ('scheduled', 'force-push'))
);
INSERT INTO runs_new (id, envoy_id, started_at, finished_at, status, resource_count, changed_count, failed_count, skipped_count, trigger)
    SELECT id, envoy_id, started_at, finished_at, status, resource_count, changed_count, failed_count, skipped_count, trigger FROM runs;

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
CREATE TEMP TABLE _m28_guard (what TEXT NOT NULL, bad INTEGER NOT NULL CHECK (bad = 0));
INSERT INTO _m28_guard SELECT 'foreign_key_check', count(*) FROM pragma_foreign_key_check;
INSERT INTO _m28_guard SELECT 'integrity_check', count(*) FROM pragma_integrity_check WHERE integrity_check <> 'ok';
DROP TABLE _m28_guard;
