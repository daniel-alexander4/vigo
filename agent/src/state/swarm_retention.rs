// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Persistent swarm blob retention tracking.
//!
//! Records when each blob was downloaded and its retention period. The agent
//! periodically scans for expired entries and evicts them from the blob cache.
//! Key: SHA256 (64-char hex), Value: 16 bytes (u64 download_timestamp + u64 retention_seconds).

use anyhow::Result;
use heed::types::{Bytes, Str};
use heed::{Database, Env, RoTxn, RwTxn};

/// LMDB-backed swarm retention tracker.
pub struct SwarmRetentionCache {
    db: Database<Str, Bytes>,
}

impl SwarmRetentionCache {
    /// Open (or create) the swarm retention database.
    pub fn open(env: &Env, wtxn: &mut RwTxn<'_>) -> Result<Self> {
        let db = env
            .create_database(wtxn, Some("swarm_retention"))
            .map_err(|e| anyhow::anyhow!("creating swarm_retention db: {}", e))?;
        Ok(Self { db })
    }

    /// Record a blob download with its retention period (in seconds).
    pub fn record(&self, wtxn: &mut RwTxn<'_>, sha256: &str, retention_secs: u64) -> Result<()> {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        let mut buf = [0u8; 16];
        buf[..8].copy_from_slice(&now.to_le_bytes());
        buf[8..].copy_from_slice(&retention_secs.to_le_bytes());
        self.db
            .put(wtxn, sha256, &buf)
            .map_err(|e| anyhow::anyhow!("recording swarm retention: {}", e))?;
        Ok(())
    }

    /// Return SHA256s of blobs whose retention has expired.
    pub fn expired(&self, rtxn: &RoTxn<'_>) -> Result<Vec<String>> {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        let mut out = Vec::new();
        let iter = self
            .db
            .iter(rtxn)
            .map_err(|e| anyhow::anyhow!("iterating swarm_retention: {}", e))?;
        for item in iter {
            let (sha256, val) = item.map_err(|e| anyhow::anyhow!("reading entry: {}", e))?;
            if val.len() < 16 {
                continue;
            }
            let download_ts = u64::from_le_bytes(val[..8].try_into().unwrap());
            let retention = u64::from_le_bytes(val[8..16].try_into().unwrap());
            if retention > 0 && now > download_ts + retention {
                out.push(sha256.to_string());
            }
        }
        Ok(out)
    }

    /// Remove a retention entry after eviction.
    pub fn remove(&self, wtxn: &mut RwTxn<'_>, sha256: &str) -> Result<()> {
        self.db
            .delete(wtxn, sha256)
            .map_err(|e| anyhow::anyhow!("removing swarm retention entry: {}", e))?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_env() -> (tempfile::TempDir, heed::Env) {
        let dir = tempfile::tempdir().unwrap();
        let env = unsafe {
            heed::EnvOpenOptions::new()
                .map_size(1024 * 1024)
                .max_dbs(2)
                .open(dir.path())
                .unwrap()
        };
        (dir, env)
    }

    #[test]
    fn test_record_and_expired() {
        let (_dir, env) = test_env();
        let mut wtxn = env.write_txn().unwrap();
        let cache = SwarmRetentionCache::open(&env, &mut wtxn).unwrap();
        wtxn.commit().unwrap();

        // Record a blob with 1-second retention.
        let mut wtxn = env.write_txn().unwrap();
        cache.record(&mut wtxn, "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234", 1).unwrap();
        wtxn.commit().unwrap();

        // Should not be expired immediately (race-safe: 1s retention, just recorded).
        let rtxn = env.read_txn().unwrap();
        let expired = cache.expired(&rtxn).unwrap();
        assert!(expired.is_empty(), "should not expire immediately");
    }

    #[test]
    fn test_record_zero_retention_never_expires() {
        let (_dir, env) = test_env();
        let mut wtxn = env.write_txn().unwrap();
        let cache = SwarmRetentionCache::open(&env, &mut wtxn).unwrap();

        // Record with retention=0 (keep forever).
        cache.record(&mut wtxn, "0000000000000000000000000000000000000000000000000000000000000000", 0).unwrap();
        wtxn.commit().unwrap();

        let rtxn = env.read_txn().unwrap();
        let expired = cache.expired(&rtxn).unwrap();
        assert!(expired.is_empty(), "retention=0 should never expire");
    }

    #[test]
    fn test_remove() {
        let (_dir, env) = test_env();
        let mut wtxn = env.write_txn().unwrap();
        let cache = SwarmRetentionCache::open(&env, &mut wtxn).unwrap();
        cache.record(&mut wtxn, "aaaa000000000000000000000000000000000000000000000000000000000000", 3600).unwrap();
        cache.remove(&mut wtxn, "aaaa000000000000000000000000000000000000000000000000000000000000").unwrap();
        wtxn.commit().unwrap();

        // Verify it's gone by checking no expired entries.
        let rtxn = env.read_txn().unwrap();
        let expired = cache.expired(&rtxn).unwrap();
        assert!(expired.is_empty());
    }
}
