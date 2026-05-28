-- Add `waived` column so the daily snapshot (and the dashboard summary
-- reading from it) preserves the waiver count alongside satisfied /
-- partial / not_satisfied. Pre-existing rows default to 0 — slightly
-- understates waivers for any historical snapshot, but the next
-- scheduled or publish-triggered snapshot overwrites with the correct
-- value via the (snapshot_date, framework) UPSERT key.
ALTER TABLE compliance_history ADD COLUMN waived INTEGER NOT NULL DEFAULT 0;
