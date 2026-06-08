-- Revert approved_patterns.used_by to ON DELETE NO ACTION (the original 000001
-- shape, no ON DELETE clause).
CREATE TABLE approved_patterns_old (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    pattern    TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    used_at    DATETIME,
    used_by    TEXT REFERENCES envoys (id)
);
INSERT INTO approved_patterns_old (id, pattern, token_hash, created_at, used_at, used_by)
    SELECT id, pattern, token_hash, created_at, used_at, used_by FROM approved_patterns;
DROP TABLE approved_patterns;
ALTER TABLE approved_patterns_old RENAME TO approved_patterns;
