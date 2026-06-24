-- Reverse 000024: drop the enrolled_at-ordered envoy list index.
DROP INDEX IF EXISTS idx_envoys_enrolled_at_id;
