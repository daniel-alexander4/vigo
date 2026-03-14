// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Persistent server public key storage for bundle signature verification.

use anyhow::Result;
use heed::types::{Bytes, Str};
use heed::{Database, Env, RoTxn, RwTxn};

const KEY_PEM: &str = "server_pubkey";

/// LMDB-backed server public key cache.
pub struct ServerKeyCache {
    db: Database<Str, Bytes>,
}

impl ServerKeyCache {
    /// Open (or create) the server key cache database.
    pub fn open(env: &Env, wtxn: &mut RwTxn<'_>) -> Result<Self> {
        let db = env
            .create_database(wtxn, Some("server_key"))
            .map_err(|e| anyhow::anyhow!("creating server_key db: {}", e))?;
        Ok(Self { db })
    }

    /// Store the server's public key PEM.
    pub fn store(&self, wtxn: &mut RwTxn<'_>, pem: &str) -> Result<()> {
        self.db.put(wtxn, KEY_PEM, pem.as_bytes())?;
        Ok(())
    }

    /// Load the server's public key PEM. Returns None if not stored.
    pub fn load(&self, rtxn: &RoTxn<'_>) -> Result<Option<String>> {
        match self.db.get(rtxn, KEY_PEM)? {
            Some(b) => Ok(Some(String::from_utf8_lossy(b).into_owned())),
            None => Ok(None),
        }
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn store_and_load() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let pem = "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA...\n-----END PUBLIC KEY-----\n";

        let mut wtxn = store.write_txn().unwrap();
        store.server_key.store(&mut wtxn, pem).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        let loaded = store.server_key.load(&rtxn).unwrap().unwrap();
        assert_eq!(loaded, pem);
    }

    #[test]
    fn load_empty() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let rtxn = store.read_txn().unwrap();
        assert!(store.server_key.load(&rtxn).unwrap().is_none());
    }
}
