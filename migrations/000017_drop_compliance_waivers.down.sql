-- Recreate the compliance-waiver table (mirrors 000001_initial_schema).
CREATE TABLE IF NOT EXISTS compliance_waivers (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    framework   TEXT NOT NULL,
    control_id  TEXT NOT NULL,
    reason      TEXT NOT NULL,
    approver    TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at  DATETIME,
    active      INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_compliance_waivers_framework ON compliance_waivers (framework, control_id);
CREATE INDEX IF NOT EXISTS idx_compliance_waivers_active ON compliance_waivers (active);
