-- Rename the module-concept columns to configcrate (module → configcrate
-- rename, 0.71.0). RENAME COLUMN preserves existing rows. The config *system*
-- (server.yaml, the loader) is unaffected — these columns store the name of a
-- configcrate (the reusable resource-set kind) per run/plan/promotion.
ALTER TABLE runs RENAME COLUMN module_count TO configcrate_count;
ALTER TABLE run_results RENAME COLUMN module_name TO configcrate_name;
ALTER TABLE plan_results RENAME COLUMN module_name TO configcrate_name;
ALTER TABLE env_promotions RENAME COLUMN module_name TO configcrate_name;
ALTER TABLE plans RENAME COLUMN modules TO configcrates;
