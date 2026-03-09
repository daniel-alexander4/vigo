-- Add ON DELETE CASCADE to all foreign keys referencing nodes(id).
-- This allows DELETE FROM nodes WHERE id = $1 to clean up all related data.

ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_client_id_fkey;
ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_node_id_fkey;
ALTER TABLE runs ADD CONSTRAINT runs_node_id_fkey FOREIGN KEY (node_id) REFERENCES nodes (id) ON DELETE CASCADE;

ALTER TABLE node_nails DROP CONSTRAINT IF EXISTS client_facts_client_id_fkey;
ALTER TABLE node_nails DROP CONSTRAINT IF EXISTS node_nails_node_id_fkey;
ALTER TABLE node_nails ADD CONSTRAINT node_nails_node_id_fkey FOREIGN KEY (node_id) REFERENCES nodes (id) ON DELETE CASCADE;

ALTER TABLE plans DROP CONSTRAINT IF EXISTS plans_client_id_fkey;
ALTER TABLE plans DROP CONSTRAINT IF EXISTS plans_node_id_fkey;
ALTER TABLE plans ADD CONSTRAINT plans_node_id_fkey FOREIGN KEY (node_id) REFERENCES nodes (id) ON DELETE CASCADE;

ALTER TABLE file_snapshots DROP CONSTRAINT IF EXISTS file_snapshots_client_id_fkey;
ALTER TABLE file_snapshots DROP CONSTRAINT IF EXISTS file_snapshots_node_id_fkey;
ALTER TABLE file_snapshots ADD CONSTRAINT file_snapshots_node_id_fkey FOREIGN KEY (node_id) REFERENCES nodes (id) ON DELETE CASCADE;

ALTER TABLE package_audit DROP CONSTRAINT IF EXISTS package_audit_client_id_fkey;
ALTER TABLE package_audit DROP CONSTRAINT IF EXISTS package_audit_node_id_fkey;
ALTER TABLE package_audit ADD CONSTRAINT package_audit_node_id_fkey FOREIGN KEY (node_id) REFERENCES nodes (id) ON DELETE CASCADE;

-- Also cascade through child tables of runs and plans.
ALTER TABLE run_results DROP CONSTRAINT IF EXISTS run_results_run_id_fkey;
ALTER TABLE run_results ADD CONSTRAINT run_results_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs (id) ON DELETE CASCADE;

ALTER TABLE plan_results DROP CONSTRAINT IF EXISTS plan_results_plan_id_fkey;
ALTER TABLE plan_results ADD CONSTRAINT plan_results_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES plans (id) ON DELETE CASCADE;
