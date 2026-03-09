-- Revert CASCADE back to plain foreign keys.

ALTER TABLE runs DROP CONSTRAINT runs_node_id_fkey;
ALTER TABLE runs ADD CONSTRAINT runs_node_id_fkey FOREIGN KEY (node_id) REFERENCES nodes (id);

ALTER TABLE node_nails DROP CONSTRAINT node_nails_node_id_fkey;
ALTER TABLE node_nails ADD CONSTRAINT node_nails_node_id_fkey FOREIGN KEY (node_id) REFERENCES nodes (id);

ALTER TABLE plans DROP CONSTRAINT plans_node_id_fkey;
ALTER TABLE plans ADD CONSTRAINT plans_node_id_fkey FOREIGN KEY (node_id) REFERENCES nodes (id);

ALTER TABLE file_snapshots DROP CONSTRAINT file_snapshots_node_id_fkey;
ALTER TABLE file_snapshots ADD CONSTRAINT file_snapshots_node_id_fkey FOREIGN KEY (node_id) REFERENCES nodes (id);

ALTER TABLE package_audit DROP CONSTRAINT package_audit_node_id_fkey;
ALTER TABLE package_audit ADD CONSTRAINT package_audit_node_id_fkey FOREIGN KEY (node_id) REFERENCES nodes (id);

ALTER TABLE run_results DROP CONSTRAINT run_results_run_id_fkey;
ALTER TABLE run_results ADD CONSTRAINT run_results_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs (id);

ALTER TABLE plan_results DROP CONSTRAINT plan_results_plan_id_fkey;
ALTER TABLE plan_results ADD CONSTRAINT plan_results_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES plans (id);
