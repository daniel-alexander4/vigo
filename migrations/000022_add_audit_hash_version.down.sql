-- Reverse 000022: drop the per-row audit hash-scheme version column.
ALTER TABLE audit_entries DROP COLUMN hash_version;
