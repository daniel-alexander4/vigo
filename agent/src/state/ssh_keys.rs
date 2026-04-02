// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Persistent SSH host key storage for network device TOFU verification.
//!
//! Keys are stored by "{host}:{port}" and contain the base64-encoded
//! public key. On reconnect, the stored key is compared against the
//! server's presented key to detect MITM attacks.

use anyhow::Result;
use heed::types::{Str, Bytes};
use heed::{Database, Env, RoTxn, RwTxn};

/// LMDB-backed SSH host key cache.
pub struct SshKeysCache {
    db: Database<Str, Bytes>,
}

impl SshKeysCache {
    /// Open (or create) the SSH keys cache database.
    pub fn open(env: &Env, wtxn: &mut RwTxn<'_>) -> Result<Self> {
        let db = env
            .create_database(wtxn, Some("ssh_keys"))
            .map_err(|e| anyhow::anyhow!("creating ssh_keys db: {}", e))?;
        Ok(Self { db })
    }

    /// Store a host key. The key_id is "{host}:{port}".
    pub fn store(&self, wtxn: &mut RwTxn<'_>, key_id: &str, pubkey_b64: &str) -> Result<()> {
        self.db.put(wtxn, key_id, pubkey_b64.as_bytes())?;
        Ok(())
    }

    /// Load a host key by "{host}:{port}". Returns None if not stored.
    pub fn load(&self, rtxn: &RoTxn<'_>, key_id: &str) -> Result<Option<String>> {
        match self.db.get(rtxn, key_id)? {
            Some(b) => Ok(Some(String::from_utf8_lossy(b).into_owned())),
            None => Ok(None),
        }
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn test_ssh_key_cache_store_load() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let key_id = "10.0.0.1:22";
        let pubkey = "AAAAC3NzaC1lZDI1NTE5AAAAIExampleKey";

        let mut wtxn = store.write_txn().unwrap();
        store.ssh_keys.store(&mut wtxn, key_id, pubkey).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        let loaded = store.ssh_keys.load(&rtxn, key_id).unwrap().unwrap();
        assert_eq!(loaded, pubkey);
    }

    #[test]
    fn test_ssh_key_cache_load_empty() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let rtxn = store.read_txn().unwrap();
        assert!(store.ssh_keys.load(&rtxn, "unknown:22").unwrap().is_none());
    }
}
