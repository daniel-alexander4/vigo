-- SQLite doesn't support DROP COLUMN before 3.35.0; recreate table.
CREATE TABLE thralls_backup AS SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push FROM thralls;
DROP TABLE thralls;
CREATE TABLE thralls (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    hostname    TEXT NOT NULL,
    ip_address  TEXT NOT NULL,
    pubkey_pem  TEXT NOT NULL,
    enrolled_at DATETIME NOT NULL DEFAULT (datetime('now')),
    last_seen   DATETIME,
    revoked     INTEGER NOT NULL DEFAULT 0,
    revoked_at  DATETIME,
    force_push  INTEGER NOT NULL DEFAULT 0
);
INSERT INTO thralls SELECT * FROM thralls_backup;
DROP TABLE thralls_backup;
CREATE INDEX IF NOT EXISTS idx_thralls_hostname ON thralls (hostname);
