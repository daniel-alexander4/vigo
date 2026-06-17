-- 000023: per-user TOTP last-consumed step, to prevent code replay (forward-only).
--
-- TOTP validated a code against the current 30s step ± skew but recorded
-- nothing, so a single valid code could be replayed for its whole validity
-- window. This stores the highest step ever consumed per user; verification
-- rejects any code whose step <= totp_last_consumed_step and advances it
-- atomically. Bolt-local like the other TOTP fields (ADR-030 direction call
-- A); no updated_at_ms bump, never replicated.
ALTER TABLE users ADD COLUMN totp_last_consumed_step INTEGER NOT NULL DEFAULT 0;
