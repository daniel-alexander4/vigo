-- Restore the users.updated_at DATETIME column with the original
-- default. Re-add gets datetime('now') per row (not original values);
-- acceptable because nothing reads the column.
ALTER TABLE users ADD COLUMN updated_at DATETIME NOT NULL DEFAULT (datetime('now'));
