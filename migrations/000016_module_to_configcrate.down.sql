-- Reverse the module → configcrate column rename.
ALTER TABLE runs RENAME COLUMN configcrate_count TO module_count;
ALTER TABLE run_results RENAME COLUMN configcrate_name TO module_name;
ALTER TABLE plan_results RENAME COLUMN configcrate_name TO module_name;
ALTER TABLE env_promotions RENAME COLUMN configcrate_name TO module_name;
ALTER TABLE plans RENAME COLUMN configcrates TO modules;
