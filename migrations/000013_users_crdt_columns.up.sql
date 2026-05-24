-- Slice 2-iii-b of ADR-030 — schema for cross-bolt webusers CRDT.
-- Adds the four columns the LWW-Map needs:
--
--   updated_at_ms — unix-ms timestamp of the last write; the CRDT
--                   comparator. Set Go-side via time.Now().UnixMilli()
--                   on every mutation (Insert/Update/Soft-delete/TOTP).
--                   Match's spanner_bolts.admitted_at's INTEGER type;
--                   SQLite's datetime('now') is second-precision and
--                   would cause artificial ties between near-simultaneous
--                   bolt writes.
--
--   origin_bolt   — pubkey-hex of the bolt that last wrote this row.
--                   Used by the LWW comparator's same-ms tiebreaker
--                   (lex-min wins) and by merge verification (only rows
--                   originating from known live roster bolts are
--                   accepted). Empty string this slice — Slice 2-iii-c
--                   wires the actual writer when signing helpers ship.
--
--   deleted_at    — soft-delete tombstone. NULL = live row; INTEGER =
--                   unix-ms of deletion. Mirrors spanner_bolts.revoked_at
--                   shape exactly. Read queries filter WHERE deleted_at
--                   IS NULL; merge propagates the tombstone as just
--                   another LWW state.
--
--   signature     — ed25519 signature over the row's canonical content
--                   by the origin bolt's identity. NULL = unsigned
--                   (legacy / pre-replication / this slice). Slice
--                   2-iii-c populates it; Slice 2-iii-d's merge verifies.
--
-- The existing updated_at DATETIME column is preserved unchanged — it
-- stays the human-readable display projection. Collapsing the two
-- timestamps is tracked as a separate /discuss item; the dual shape
-- ships intentionally.
ALTER TABLE users ADD COLUMN updated_at_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN origin_bolt   TEXT    NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN deleted_at    INTEGER;
ALTER TABLE users ADD COLUMN signature     BLOB;

CREATE INDEX IF NOT EXISTS idx_users_deleted_at ON users (deleted_at);
