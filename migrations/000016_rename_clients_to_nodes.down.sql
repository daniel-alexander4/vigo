ALTER TABLE secret_rotation_events RENAME COLUMN affected_nodes TO affected_clients;

ALTER TABLE package_audit RENAME COLUMN node_id TO client_id;

ALTER TABLE file_snapshots RENAME COLUMN node_id TO client_id;

ALTER TABLE plans RENAME COLUMN node_id TO client_id;

ALTER TABLE node_nails RENAME COLUMN node_id TO client_id;
ALTER TABLE node_nails RENAME TO client_nails;

ALTER INDEX idx_runs_node_id RENAME TO idx_runs_client_id;
ALTER TABLE runs RENAME COLUMN node_id TO client_id;

ALTER INDEX idx_nodes_hostname RENAME TO idx_clients_hostname;
ALTER TABLE nodes RENAME TO clients;
