ALTER TABLE convergence_history RENAME COLUMN no_data TO unknown;
ALTER TABLE convergence_history RENAME COLUMN lost_contact TO stale;
ALTER TABLE convergence_history RENAME COLUMN errors TO failing;
ALTER TABLE convergence_history RENAME COLUMN recurring_drift TO drifted;
ALTER TABLE convergence_history RENAME COLUMN converged TO compliant;
ALTER TABLE convergence_history RENAME TO compliance_history;
