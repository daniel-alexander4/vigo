-- 000022: version the audit hash scheme (forward-only).
--
-- The pre-version computeHash concatenated fields with no length-prefix or
-- delimiter, so distinct field tuples could collide
-- (actor="ab",source="c" == actor="a",source="bc"). The fix is a delimited
-- (length-prefixed) V2 scheme — but old rows were hashed with V1 and must
-- keep verifying against their stored hash, so the scheme is recorded
-- per-row. Existing rows default to 1 (V1); new rows write 2 (V2).
ALTER TABLE audit_entries ADD COLUMN hash_version INTEGER NOT NULL DEFAULT 1;
