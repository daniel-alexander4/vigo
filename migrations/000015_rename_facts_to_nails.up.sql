ALTER TABLE client_facts RENAME TO client_nails;
ALTER TABLE client_nails RENAME COLUMN facts TO nails;
