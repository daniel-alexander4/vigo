// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Persistent traits cache with TTL.
//!
//! Stores the latest traits JSON snapshot with a timestamp. When the agent
//! is offline, it can use the cached traits for policy evaluation if the
//! snapshot is within the TTL.

use anyhow::Result;
use heed::types::{Bytes, Str};
use heed::{Database, Env, RoTxn, RwTxn};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

/// Key for the traits JSON blob.
const TRAITS_KEY: &str = "traits";
/// Key for the timestamp (Unix seconds).
const TIMESTAMP_KEY: &str = "traits_ts";

/// LMDB-backed traits cache.
pub struct TraitsCache {
    db: Database<Str, Bytes>,
}

impl TraitsCache {
    /// Open (or create) the traits cache database.
    pub fn open(env: &Env, wtxn: &mut RwTxn<'_>) -> Result<Self> {
        let db = env
            .create_database(wtxn, Some("traits"))
            .map_err(|e| anyhow::anyhow!("creating traits db: {}", e))?;
        Ok(Self { db })
    }

    /// Store a traits JSON snapshot with the current timestamp.
    pub fn store(&self, wtxn: &mut RwTxn<'_>, traits_json: &[u8]) -> Result<()> {
        self.db.put(wtxn, TRAITS_KEY, traits_json)?;
        let ts = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        self.db.put(wtxn, TIMESTAMP_KEY, &ts.to_le_bytes())?;
        Ok(())
    }

    /// Load traits JSON if it exists and is within the given TTL.
    /// Returns None if no traits are cached or the cache has expired.
    pub fn load(&self, rtxn: &RoTxn<'_>, ttl: Duration) -> Result<Option<Vec<u8>>> {
        let ts_bytes = match self.db.get(rtxn, TIMESTAMP_KEY)? {
            Some(b) if b.len() == 8 => {
                let mut arr = [0u8; 8];
                arr.copy_from_slice(b);
                u64::from_le_bytes(arr)
            }
            _ => return Ok(None),
        };

        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();

        if ttl.is_zero() || now.saturating_sub(ts_bytes) > ttl.as_secs() {
            return Ok(None);
        }

        match self.db.get(rtxn, TRAITS_KEY)? {
            Some(b) => Ok(Some(b.to_vec())),
            None => Ok(None),
        }
    }

    /// Load traits JSON regardless of TTL (for offline convergence).
    pub fn load_any(&self, rtxn: &RoTxn<'_>) -> Result<Option<Vec<u8>>> {
        match self.db.get(rtxn, TRAITS_KEY)? {
            Some(b) => Ok(Some(b.to_vec())),
            None => Ok(None),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn store_and_load_within_ttl() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let traits = br#"{"os":{"family":"debian"}}"#;

        let mut wtxn = store.write_txn().unwrap();
        store.traits.store(&mut wtxn, traits).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        let loaded = store
            .traits
            .load(&rtxn, Duration::from_secs(3600))
            .unwrap()
            .unwrap();
        assert_eq!(loaded, traits);
    }

    #[test]
    fn load_expired() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let traits = b"{}";

        let mut wtxn = store.write_txn().unwrap();
        store.traits.store(&mut wtxn, traits).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        // TTL of 0 seconds means everything is expired.
        assert!(store
            .traits
            .load(&rtxn, Duration::from_secs(0))
            .unwrap()
            .is_none());
    }

    #[test]
    fn load_any_ignores_ttl() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let traits = b"{}";

        let mut wtxn = store.write_txn().unwrap();
        store.traits.store(&mut wtxn, traits).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        assert!(store.traits.load_any(&rtxn).unwrap().is_some());
    }

    #[test]
    fn load_empty() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let rtxn = store.read_txn().unwrap();
        assert!(store
            .traits
            .load(&rtxn, Duration::from_secs(3600))
            .unwrap()
            .is_none());
    }
}
