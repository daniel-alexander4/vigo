ALTER TABLE compliance_history RENAME TO convergence_history;
ALTER TABLE convergence_history RENAME COLUMN compliant TO converged;
ALTER TABLE convergence_history RENAME COLUMN drifted TO recurring_drift;
ALTER TABLE convergence_history RENAME COLUMN failing TO errors;
ALTER TABLE convergence_history RENAME COLUMN stale TO lost_contact;
ALTER TABLE convergence_history RENAME COLUMN unknown TO no_data;
