-- Reverse 000025: drop the cert-pin column.
ALTER TABLE envoys DROP COLUMN cert_spki_sha256;
