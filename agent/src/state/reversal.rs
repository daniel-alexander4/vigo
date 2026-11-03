// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Persistent resource-reversal store.
//!
//! Backs the `revert:` lever for the non-state resource types (ADR pending).
//! Two kinds of record, both keyed by `"<type>\0<resource-name>"`:
//!
//! - **Group B (value-setters: hostname, timezone)** capture the live value at
//!   first forward apply — write-once-guarded — so `revert: true` can restore
//!   the operator's machine to what Vigo first found. `snapshot` holds that
//!   prior value.
//! - **Group A (local-command: exec, backup, db_backup, replace)** carry no
//!   snapshot — their inverse is the operator's `on_revert:` command. Only the
//!   `reverted` marker is used.
//!
//! The `reverted` marker makes revert idempotent: once a resource is reverted
//! it stays a green no-op until a forward apply re-arms it (clears the marker,
//! and for Group B re-captures a fresh snapshot). See [`ReversalEntry`].

use anyhow::Result;
use heed::types::{Bytes, Str};
use heed::{Database, Env, RoTxn, RwTxn};

/// A reversal record for one resource.
#[derive(Debug, Clone, PartialEq)]
pub struct ReversalEntry {
    /// The value Vigo first found, captured at first forward apply. `Some` for
    /// captured Group B resources; `None` for Group A (command-reverted, no
    /// snapshot) and for Group B before capture.
    pub snapshot: Option<String>,
    /// Set once a `revert: true` has been honored. Makes re-evaluation an
    /// idempotent no-op; cleared on the next forward apply (re-arm).
    pub reverted: bool,
}

impl ReversalEntry {
    /// Serialize to: `[reverted u8][has_snapshot u8][snapshot utf8...]`.
    fn to_bytes(&self) -> Vec<u8> {
        let mut buf = Vec::with_capacity(2 + self.snapshot.as_ref().map_or(0, |s| s.len()));
        buf.push(self.reverted as u8);
        match &self.snapshot {
            Some(s) => {
                buf.push(1);
                buf.extend_from_slice(s.as_bytes());
            }
            None => buf.push(0),
        }
        buf
    }

    /// Deserialize from the [`Self::to_bytes`] layout. Returns `None` on a
    /// malformed/truncated record (treated as "no entry" by the caller).
    fn from_bytes(b: &[u8]) -> Option<Self> {
        if b.len() < 2 {
            return None;
        }
        let reverted = b[0] != 0;
        let snapshot = if b[1] != 0 {
            Some(String::from_utf8_lossy(&b[2..]).into_owned())
        } else {
            None
        };
        Some(Self { snapshot, reverted })
    }
}

/// LMDB-backed reversal store.
pub struct ReversalCache {
    db: Database<Str, Bytes>,
}

impl ReversalCache {
    /// Open (or create) the reversal database.
    pub fn open(env: &Env, wtxn: &mut RwTxn<'_>) -> Result<Self> {
        let db = env
            .create_database(wtxn, Some("reversal"))
            .map_err(|e| anyhow::anyhow!("creating reversal db: {}", e))?;
        Ok(Self { db })
    }

    /// Stable per-resource key: `"<type>\0<resource-name>"`.
    pub fn key(resource_type: &str, resource_name: &str) -> String {
        format!("{}\0{}", resource_type, resource_name)
    }

    /// Get the reversal record for a resource key.
    pub fn get(&self, rtxn: &RoTxn<'_>, key: &str) -> Result<Option<ReversalEntry>> {
        match self.db.get(rtxn, key)? {
            Some(bytes) => Ok(ReversalEntry::from_bytes(bytes)),
            None => Ok(None),
        }
    }

    /// Store a reversal record for a resource key.
    pub fn put(&self, wtxn: &mut RwTxn<'_>, key: &str, entry: &ReversalEntry) -> Result<()> {
        self.db.put(wtxn, key, &entry.to_bytes())?;
        Ok(())
    }

    /// Remove a reversal record.
    pub fn delete(&self, wtxn: &mut RwTxn<'_>, key: &str) -> Result<()> {
        self.db.delete(wtxn, key)?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip_snapshot_entry() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();
        let key = ReversalCache::key("hostname", "set-hostname");
        let entry = ReversalEntry {
            snapshot: Some("ubuntu-default".into()),
            reverted: false,
        };

        let mut wtxn = store.write_txn().unwrap();
        store.reversal.put(&mut wtxn, &key, &entry).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        assert_eq!(store.reversal.get(&rtxn, &key).unwrap().unwrap(), entry);
    }

    #[test]
    fn roundtrip_command_marker_no_snapshot() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();
        let key = ReversalCache::key("exec", "add-rule");
        let entry = ReversalEntry {
            snapshot: None,
            reverted: true,
        };

        let mut wtxn = store.write_txn().unwrap();
        store.reversal.put(&mut wtxn, &key, &entry).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        let loaded = store.reversal.get(&rtxn, &key).unwrap().unwrap();
        assert_eq!(loaded, entry);
        assert!(loaded.snapshot.is_none());
        assert!(loaded.reverted);
    }

    #[test]
    fn get_missing_is_none() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();
        let rtxn = store.read_txn().unwrap();
        assert!(store
            .reversal
            .get(&rtxn, &ReversalCache::key("timezone", "tz"))
            .unwrap()
            .is_none());
    }

    #[test]
    fn delete_clears_entry() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();
        let key = ReversalCache::key("timezone", "tz");
        let entry = ReversalEntry { snapshot: Some("UTC".into()), reverted: false };

        let mut wtxn = store.write_txn().unwrap();
        store.reversal.put(&mut wtxn, &key, &entry).unwrap();
        store.reversal.delete(&mut wtxn, &key).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        assert!(store.reversal.get(&rtxn, &key).unwrap().is_none());
    }
}
