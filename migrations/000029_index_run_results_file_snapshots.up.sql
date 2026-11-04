-- 000029: indexes for the run_results / file_snapshots filters and cascades
-- (forward-only; index creation only, no table rebuild, so no rows move).
--
-- file_snapshots has never had an index (000019, 000027 and 000028 each
-- rebuilt it without one):
--   * run_id: file_snapshots.run_id REFERENCES runs ON DELETE CASCADE, so every
--     retention delete of a run (PruneRunsByAge) scanned the whole table to find
--     its children.
--   * (envoy_id, file_path, created_at): ListFileSnapshots filters on
--     envoy_id + file_path and orders by created_at DESC; the composite serves
--     the filter and the sort, and its leading envoy_id column also serves the
--     envoy_id ON DELETE CASCADE.
--   * created_at: PruneFileSnapshotsByAge deletes WHERE created_at < ?.
-- run_results:
--   * created_at: ListRunResultsSince(ByEnvoy) filter on rr.created_at >= ? and
--     order by it DESC with a LIMIT (the SIEM export).
--   * configcrate_name: EnvoysWithConfigcrate filters on it (configcrate
--     retraction preview).
--
-- No ANALYZE: with no sqlite_stat1 rows SQLite's default estimates favor an
-- equality/range index, and a one-shot ANALYZE taken at migration time would go
-- stale as these append-only tables grow. The plans are asserted by
-- EXPLAIN QUERY PLAN in migrations/indexes_000029_test.go.
CREATE INDEX IF NOT EXISTS idx_file_snapshots_run_id ON file_snapshots (run_id);
CREATE INDEX IF NOT EXISTS idx_file_snapshots_envoy_path_created ON file_snapshots (envoy_id, file_path, created_at);
CREATE INDEX IF NOT EXISTS idx_file_snapshots_created_at ON file_snapshots (created_at);
CREATE INDEX IF NOT EXISTS idx_run_results_created_at ON run_results (created_at);
CREATE INDEX IF NOT EXISTS idx_run_results_configcrate_name ON run_results (configcrate_name);
