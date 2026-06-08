-- 000020: fix the envoy-delete-blocking foreign key on approved_patterns.
--
-- approved_patterns.used_by references envoys (id) with no ON DELETE clause,
-- i.e. ON DELETE NO ACTION. Token enrollment stamps used_by = <envoy id>
-- (registry.MarkPatternUsed), and DeleteEnvoy is a bare `DELETE FROM envoys`
-- with no cleanup, so deleting any pattern-enrolled envoy failed with
-- "FOREIGN KEY constraint failed" (same bug class as the 000019 cascade fix).
-- Trusted-enrollment envoys never consume a pattern, which is why the bug
-- stayed latent.
--
-- SET NULL (not CASCADE): an envoy delete should drop the dangling back-pointer
-- while preserving the record that the one-time pattern/token was consumed
-- (used_at stays stamped). used_by is nullable, so SET NULL is valid.
--
-- SQLite can't alter a foreign key in place, so rebuild the table. It has no
-- indexes, triggers, or child tables, so the create/copy/drop/rename is safe
-- with foreign keys enabled.
CREATE TABLE approved_patterns_new (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
    pattern    TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    used_at    DATETIME,
    used_by    TEXT REFERENCES envoys (id) ON DELETE SET NULL
);
INSERT INTO approved_patterns_new (id, pattern, token_hash, created_at, used_at, used_by)
    SELECT id, pattern, token_hash, created_at, used_at, used_by FROM approved_patterns;
DROP TABLE approved_patterns;
ALTER TABLE approved_patterns_new RENAME TO approved_patterns;
