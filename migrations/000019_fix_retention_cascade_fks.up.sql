-- 000019: fix retention-blocking foreign keys.
--
-- file_snapshots.run_id and workflow_step_results.task_run_id were declared
-- ON DELETE NO ACTION (no ON DELETE clause), unlike their cascade siblings
-- run_results.run_id and task_run_targets.task_run_id. As a result
-- `DELETE FROM runs` / `DELETE FROM task_runs` failed with
-- "FOREIGN KEY constraint failed" for any parent row that had a child here,
-- and SQLite rolled the whole statement back — so the retention pruner has
-- silently deleted zero runs/task_runs since the schema was introduced, and
-- run history (runs -> run_results) grew without bound.
--
-- SQLite can't alter a foreign key in place, so rebuild both (leaf) tables
-- with ON DELETE CASCADE. Both are small; nothing references either table,
-- so the create/copy/drop/rename is safe with foreign keys enabled.

-- file_snapshots --------------------------------------------------------------
CREATE TABLE file_snapshots_new (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    envoy_id    TEXT NOT NULL REFERENCES envoys (id) ON DELETE CASCADE,
    file_path    TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    content      BLOB,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    run_id       TEXT REFERENCES runs (id) ON DELETE CASCADE
);
INSERT INTO file_snapshots_new (id, envoy_id, file_path, content_hash, content, created_at, run_id)
    SELECT id, envoy_id, file_path, content_hash, content, created_at, run_id FROM file_snapshots;
DROP TABLE file_snapshots;
ALTER TABLE file_snapshots_new RENAME TO file_snapshots;

-- workflow_step_results -------------------------------------------------------
CREATE TABLE workflow_step_results_new (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    step_index      INTEGER NOT NULL,
    step_name       TEXT NOT NULL,
    task_run_id     TEXT REFERENCES task_runs (id) ON DELETE CASCADE,
    status          TEXT NOT NULL CHECK (status IN ('pending', 'running', 'complete', 'failed', 'skipped')) DEFAULT 'pending',
    started_at      DATETIME,
    finished_at     DATETIME
);
INSERT INTO workflow_step_results_new (id, workflow_run_id, step_index, step_name, task_run_id, status, started_at, finished_at)
    SELECT id, workflow_run_id, step_index, step_name, task_run_id, status, started_at, finished_at FROM workflow_step_results;
DROP TABLE workflow_step_results;
ALTER TABLE workflow_step_results_new RENAME TO workflow_step_results;
CREATE INDEX IF NOT EXISTS idx_workflow_step_results_workflow ON workflow_step_results (workflow_run_id);
