-- Add 'skipped' to task_run_targets status CHECK constraint.
-- SQLite cannot ALTER CHECK constraints, so we recreate the table.

CREATE TABLE IF NOT EXISTS task_run_targets_new (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    task_run_id  TEXT NOT NULL REFERENCES task_runs (id) ON DELETE CASCADE,
    thrall_id    TEXT NOT NULL REFERENCES thralls (id) ON DELETE CASCADE,
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

INSERT INTO task_run_targets_new SELECT * FROM task_run_targets;

DROP TABLE task_run_targets;

ALTER TABLE task_run_targets_new RENAME TO task_run_targets;

CREATE INDEX IF NOT EXISTS idx_task_run_targets_task_run_id ON task_run_targets (task_run_id);
CREATE INDEX IF NOT EXISTS idx_task_run_targets_thrall_id ON task_run_targets (thrall_id);
