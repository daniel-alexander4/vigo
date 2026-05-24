DROP INDEX IF EXISTS idx_users_deleted_at;
ALTER TABLE users DROP COLUMN signature;
ALTER TABLE users DROP COLUMN deleted_at;
ALTER TABLE users DROP COLUMN origin_bolt;
ALTER TABLE users DROP COLUMN updated_at_ms;
