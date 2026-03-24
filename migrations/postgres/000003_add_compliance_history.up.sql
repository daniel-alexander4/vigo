CREATE TABLE IF NOT EXISTS compliance_history (
    id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    snapshot_date  TEXT NOT NULL DEFAULT TO_CHAR(CURRENT_DATE, 'YYYY-MM-DD'),
    framework      TEXT NOT NULL,
    score_percent  REAL NOT NULL DEFAULT 0,
    satisfied      INTEGER NOT NULL DEFAULT 0,
    partial        INTEGER NOT NULL DEFAULT 0,
    not_satisfied  INTEGER NOT NULL DEFAULT 0,
    total_controls INTEGER NOT NULL DEFAULT 0,
    UNIQUE(snapshot_date, framework)
);
