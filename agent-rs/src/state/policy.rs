// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Persistent policy bundle cache.
//!
//! Stores the latest PolicyBundle as protobuf bytes, keyed by a fixed key.
//! Enables offline convergence when the server is unreachable.

use anyhow::Result;
use heed::types::{Bytes, Str};
use heed::{Database, Env, RoTxn, RwTxn};
use prost::Message;

/// Fixed key for the cached policy bundle.
const POLICY_KEY: &str = "current";
/// Key for the cached policy version.
const VERSION_KEY: &str = "version";

/// LMDB-backed policy bundle cache.
pub struct PolicyCache {
    db: Database<Str, Bytes>,
}

impl PolicyCache {
    /// Open (or create) the policy cache database.
    pub fn open(env: &Env, wtxn: &mut RwTxn<'_>) -> Result<Self> {
        let db = env
            .create_database(wtxn, Some("policy"))
            .map_err(|e| anyhow::anyhow!("creating policy db: {}", e))?;
        Ok(Self { db })
    }

    /// Store a policy bundle and its version.
    pub fn store(
        &self,
        wtxn: &mut RwTxn<'_>,
        version: u64,
        bundle: &crate::proto::PolicyBundle,
    ) -> Result<()> {
        let bytes = bundle.encode_to_vec();
        self.db.put(wtxn, POLICY_KEY, &bytes)?;
        self.db
            .put(wtxn, VERSION_KEY, &version.to_le_bytes())?;
        Ok(())
    }

    /// Load the cached policy bundle and its version.
    /// Returns None if no policy has been cached.
    pub fn load(
        &self,
        rtxn: &RoTxn<'_>,
    ) -> Result<Option<(u64, crate::proto::PolicyBundle)>> {
        let version_bytes = match self.db.get(rtxn, VERSION_KEY)? {
            Some(b) if b.len() == 8 => {
                let mut arr = [0u8; 8];
                arr.copy_from_slice(b);
                u64::from_le_bytes(arr)
            }
            _ => return Ok(None),
        };

        let bundle_bytes = match self.db.get(rtxn, POLICY_KEY)? {
            Some(b) => b,
            None => return Ok(None),
        };

        let bundle = crate::proto::PolicyBundle::decode(bundle_bytes)
            .map_err(|e| anyhow::anyhow!("decoding cached policy: {}", e))?;

        Ok(Some((version_bytes, bundle)))
    }
}

#[cfg(test)]
mod tests {
    use crate::proto::PolicyBundle;

    #[test]
    fn store_and_load() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let bundle = PolicyBundle {
            policy_version: 42,
            matched_pattern: "*.web".into(),
            ..Default::default()
        };

        let mut wtxn = store.write_txn().unwrap();
        store.policy.store(&mut wtxn, 42, &bundle).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        let (ver, loaded) = store.policy.load(&rtxn).unwrap().unwrap();
        assert_eq!(ver, 42);
        assert_eq!(loaded.matched_pattern, "*.web");
    }

    #[test]
    fn load_empty() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let rtxn = store.read_txn().unwrap();
        assert!(store.policy.load(&rtxn).unwrap().is_none());
    }
}
