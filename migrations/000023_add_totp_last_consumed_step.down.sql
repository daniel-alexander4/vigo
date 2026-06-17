-- Reverse 000023: drop the per-user TOTP last-consumed-step column.
ALTER TABLE users DROP COLUMN totp_last_consumed_step;
