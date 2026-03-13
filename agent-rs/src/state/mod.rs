// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Persistent LMDB-backed state store for the agent.
//!
//! Enables offline convergence by caching policy bundles, file checksums,
//! traits, and pending results across agent restarts.

pub mod checksums;
pub mod pending;
pub mod policy;
pub mod traits;

use anyhow::{Context, Result};
use heed::EnvOpenOptions;
use std::path::Path;
use tracing::info;

/// Maximum LMDB map size: 256 MB.
const MAP_SIZE: usize = 256 * 1024 * 1024;

/// Maximum number of named databases within the environment.
const MAX_DBS: u32 = 8;

/// Persistent state store backed by LMDB.
///
/// Contains separate databases for policy bundles, file checksums,
/// traits snapshots, and a pending results queue. All operations are
/// ACID-safe and survive agent restarts.
pub struct StateStore {
    env: heed::Env,
    pub policy: policy::PolicyCache,
    pub checksums: checksums::ChecksumCache,
    pub traits: traits::TraitsCache,
    pub pending: pending::PendingQueue,
}

impl StateStore {
    /// Open (or create) the state store at the given directory path.
    ///
    /// The directory is created if it does not exist. Uses 256MB map size
    /// and up to 8 named databases.
    pub fn open(dir: &Path) -> Result<Self> {
        std::fs::create_dir_all(dir)
            .with_context(|| format!("creating state directory {}", dir.display()))?;

        let env = unsafe {
            EnvOpenOptions::new()
                .map_size(MAP_SIZE)
                .max_dbs(MAX_DBS)
                .open(dir)
                .with_context(|| format!("opening LMDB at {}", dir.display()))?
        };

        let mut wtxn = env.write_txn()?;

        let policy = policy::PolicyCache::open(&env, &mut wtxn)?;
        let checksums = checksums::ChecksumCache::open(&env, &mut wtxn)?;
        let traits = traits::TraitsCache::open(&env, &mut wtxn)?;
        let pending = pending::PendingQueue::open(&env, &mut wtxn)?;

        wtxn.commit()?;

        info!(path = %dir.display(), "state store opened");

        Ok(Self {
            env,
            policy,
            checksums,
            traits,
            pending,
        })
    }

    /// Get a read transaction.
    pub fn read_txn(&self) -> Result<heed::RoTxn<'_>> {
        Ok(self.env.read_txn()?)
    }

    /// Get a write transaction.
    pub fn write_txn(&self) -> Result<heed::RwTxn<'_>> {
        Ok(self.env.write_txn()?)
    }
}
