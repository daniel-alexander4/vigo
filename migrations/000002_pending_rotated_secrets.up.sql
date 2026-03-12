-- Add pending_rotated_secrets to track which secrets have rotated but
-- not yet been delivered to the agent. JSON array of secret path strings.
-- Cleared when the check-in response includes the triggered resources.
ALTER TABLE thralls ADD COLUMN pending_rotated_secrets TEXT NOT NULL DEFAULT '[]';
