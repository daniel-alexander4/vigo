// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Persistent file checksum cache.
//!
//! Stores path → (SHA256 hash, mtime) mappings so the agent can skip
//! re-hashing unchanged files during convergence.

use anyhow::Result;
use heed::types::{Bytes, Str};
use heed::{Database, Env, RoTxn, RwTxn};

/// Entry in the checksum cache: SHA256 hash + file mtime (Unix seconds).
#[derive(Debug, Clone, PartialEq)]
pub struct ChecksumEntry {
    pub sha256: [u8; 32],
    pub mtime: i64,
}

impl ChecksumEntry {
    /// Serialize to 40 bytes: 32 (sha256) + 8 (mtime LE).
    fn to_bytes(&self) -> [u8; 40] {
        let mut buf = [0u8; 40];
        buf[..32].copy_from_slice(&self.sha256);
        buf[32..].copy_from_slice(&self.mtime.to_le_bytes());
        buf
    }

    /// Deserialize from 40 bytes.
    fn from_bytes(b: &[u8]) -> Option<Self> {
        if b.len() < 40 {
            return None;
        }
        let mut sha256 = [0u8; 32];
        sha256.copy_from_slice(&b[..32]);
        let mut mtime_bytes = [0u8; 8];
        mtime_bytes.copy_from_slice(&b[32..40]);
        Some(Self {
            sha256,
            mtime: i64::from_le_bytes(mtime_bytes),
        })
    }
}

/// LMDB-backed file checksum cache.
pub struct ChecksumCache {
    db: Database<Str, Bytes>,
}

impl ChecksumCache {
    /// Open (or create) the checksum cache database.
    pub fn open(env: &Env, wtxn: &mut RwTxn<'_>) -> Result<Self> {
        let db = env
            .create_database(wtxn, Some("checksums"))
            .map_err(|e| anyhow::anyhow!("creating checksums db: {}", e))?;
        Ok(Self { db })
    }

    /// Get the cached checksum for a file path.
    pub fn get(&self, rtxn: &RoTxn<'_>, path: &str) -> Result<Option<ChecksumEntry>> {
        match self.db.get(rtxn, path)? {
            Some(bytes) => Ok(ChecksumEntry::from_bytes(bytes)),
            None => Ok(None),
        }
    }

    /// Store a checksum entry for a file path.
    pub fn put(
        &self,
        wtxn: &mut RwTxn<'_>,
        path: &str,
        entry: &ChecksumEntry,
    ) -> Result<()> {
        self.db.put(wtxn, path, &entry.to_bytes())?;
        Ok(())
    }

    /// Remove a checksum entry.
    pub fn delete(&self, wtxn: &mut RwTxn<'_>, path: &str) -> Result<()> {
        self.db.delete(wtxn, path)?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn store_and_retrieve() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let entry = ChecksumEntry {
            sha256: [0xAB; 32],
            mtime: 1700000000,
        };

        let mut wtxn = store.write_txn().unwrap();
        store
            .checksums
            .put(&mut wtxn, "/etc/nginx.conf", &entry)
            .unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        let loaded = store
            .checksums
            .get(&rtxn, "/etc/nginx.conf")
            .unwrap()
            .unwrap();
        assert_eq!(loaded, entry);
    }

    #[test]
    fn get_missing() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let rtxn = store.read_txn().unwrap();
        assert!(store
            .checksums
            .get(&rtxn, "/nonexistent")
            .unwrap()
            .is_none());
    }

    #[test]
    fn delete_entry() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let entry = ChecksumEntry {
            sha256: [0xCD; 32],
            mtime: 1700000001,
        };

        let mut wtxn = store.write_txn().unwrap();
        store
            .checksums
            .put(&mut wtxn, "/tmp/test", &entry)
            .unwrap();
        store.checksums.delete(&mut wtxn, "/tmp/test").unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        assert!(store.checksums.get(&rtxn, "/tmp/test").unwrap().is_none());
    }
}
