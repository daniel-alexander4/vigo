-- The runs.configcrate_count column has always stored the per-resource result
-- count (len(ReportResult.results)), not a configcrate count — convergence is
-- measured per resource. Rename the column to match (resource-granular
-- convergence; supersedes the module→configcrate rename for this column only).
-- RENAME COLUMN preserves existing rows. run_results.configcrate_name stays —
-- it correctly names each resource's parent configcrate.
ALTER TABLE runs RENAME COLUMN configcrate_count TO resource_count;
