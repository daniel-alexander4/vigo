-- Per-framework standards compliance scores, snapshotted daily.
CREATE TABLE IF NOT EXISTS compliance_history (
    id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    snapshot_date  TEXT NOT NULL DEFAULT (date('now')),
    framework      TEXT NOT NULL,
    score_percent  REAL NOT NULL DEFAULT 0,
    satisfied      INTEGER NOT NULL DEFAULT 0,
    partial        INTEGER NOT NULL DEFAULT 0,
    not_satisfied  INTEGER NOT NULL DEFAULT 0,
    total_controls INTEGER NOT NULL DEFAULT 0,
    UNIQUE(snapshot_date, framework)
);
