-- The convergence rename (compliance→convergence for module state) renamed
-- this table in the migration source but existing DBs still have the old name.
-- Rename table and columns to match current code.
ALTER TABLE compliance_history RENAME TO convergence_history;
ALTER TABLE convergence_history RENAME COLUMN compliant TO converged;
ALTER TABLE convergence_history RENAME COLUMN drifted TO recurring_drift;
ALTER TABLE convergence_history RENAME COLUMN failing TO errors;
ALTER TABLE convergence_history RENAME COLUMN stale TO lost_contact;
ALTER TABLE convergence_history RENAME COLUMN unknown TO no_data;
