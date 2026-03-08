-- Rename primary table
ALTER TABLE clients RENAME TO nodes;
ALTER INDEX idx_clients_hostname RENAME TO idx_nodes_hostname;

-- Rename foreign key columns and constraints across all tables
ALTER TABLE runs RENAME COLUMN client_id TO node_id;
ALTER INDEX idx_runs_client_id RENAME TO idx_runs_node_id;

ALTER TABLE client_nails RENAME TO node_nails;
ALTER TABLE node_nails RENAME COLUMN client_id TO node_id;

ALTER TABLE plans RENAME COLUMN client_id TO node_id;

ALTER TABLE file_snapshots RENAME COLUMN client_id TO node_id;

ALTER TABLE package_audit RENAME COLUMN client_id TO node_id;

-- Rename the column in secret_rotation_events
ALTER TABLE secret_rotation_events RENAME COLUMN affected_clients TO affected_nodes;
