// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Consumer subscription state for poolq (ADR-029).
//!
//! Per-user, **never replicated** — that's what makes the Kafka-style
//! model fit a CRDT substrate with no consensus: each consumer owns its
//! cursor, and replaying a topic is free (rewind the local offset).
//!
//! ### State
//!
//! `~/.vigo-poolq/subscriptions.json` holds one entry per topic_id the
//! user has subscribed to:
//!
//! ```jsonc
//! {
//!   "subscriptions": {
//!     "<topic_id_hex>": {
//!       "topic_name": "build-tasks",
//!       "offset_seq": 17,         // last-consumed seq; next read starts at +1
//!       "filter": null            // optional filter expression (post-v1)
//!     }
//!   }
//! }
//! ```
//!
//! `offset_seq = 0` means "never consumed" — the next `read` returns from
//! seq=1 (the founder's first issued seq, see [allocator::FIRST_SEQ]).
//!
//! ### Atomicity
//!
//! Each load → mutate → save round trips through [`atomic_write`], so a
//! crash mid-read never corrupts the file. Concurrent `read`/`seek`
//! invocations from the same user (two terminals) flock the
//! subscriptions file via the same [`SeqAllocator`]-style guard — added
//! when the v2 multi-process scenarios warrant it. v1 single-process-per-
//! user is the common case.

use std::collections::BTreeMap;
use std::path::Path;

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};

use super::state::{atomic_write, user_state_dir};

/// `~/.vigo-poolq/subscriptions.json`.
pub fn subscriptions_path(home: &Path) -> std::path::PathBuf {
    user_state_dir(home).join("subscriptions.json")
}

/// One consumer subscription.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct Subscription {
    /// Human-readable topic name, mirrored from the producer side for
    /// operator-friendly listing — never used as the key (`topic_id` is
    /// the only stable identity).
    #[serde(default)]
    pub topic_name: String,
    /// Last consumed `seq`. The next `read` advances from this; `seek`
    /// overwrites it; rewinding to 0 replays the topic from the start.
    #[serde(default)]
    pub offset_seq: u64,
    /// Optional filter expression — reserved for post-v1. Carried
    /// verbatim, never evaluated by the CLI today.
    #[serde(default)]
    pub filter: Option<String>,
}

/// On-disk envelope. The single map field leaves room for additional
/// fields (e.g. `version`) without bumping the file shape.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct SubscriptionsFile {
    #[serde(default)]
    pub subscriptions: BTreeMap<String, Subscription>,
}

/// Read the user's subscriptions file. A missing file ⇒ empty map (a
/// fresh user has nothing subscribed yet, no failure).
pub fn load(home: &Path) -> Result<SubscriptionsFile> {
    let path = subscriptions_path(home);
    let bytes = match std::fs::read(&path) {
        Ok(b) => b,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            return Ok(SubscriptionsFile::default())
        }
        Err(e) => {
            return Err(anyhow::Error::from(e))
                .with_context(|| format!("reading {}", path.display()))
        }
    };
    if bytes.is_empty() {
        return Ok(SubscriptionsFile::default());
    }
    serde_json::from_slice(&bytes)
        .with_context(|| format!("parsing {}", path.display()))
}

/// Write the user's subscriptions file atomically. Creates
/// `~/.vigo-poolq/` if missing.
pub fn save(home: &Path, file: &SubscriptionsFile) -> Result<()> {
    std::fs::create_dir_all(user_state_dir(home))
        .with_context(|| format!("creating {}", user_state_dir(home).display()))?;
    let bytes = serde_json::to_vec_pretty(file).context("serializing subscriptions")?;
    atomic_write(&subscriptions_path(home), &bytes)
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn missing_file_loads_as_empty() {
        let dir = TempDir::new().unwrap();
        let got = load(dir.path()).unwrap();
        assert!(got.subscriptions.is_empty());
    }

    #[test]
    fn round_trip_preserves_entries() {
        let dir = TempDir::new().unwrap();
        let mut f = SubscriptionsFile::default();
        f.subscriptions.insert(
            "ab".repeat(32),
            Subscription {
                topic_name: "build-tasks".into(),
                offset_seq: 17,
                filter: None,
            },
        );
        save(dir.path(), &f).unwrap();
        let got = load(dir.path()).unwrap();
        assert_eq!(got, f);
    }

    #[test]
    fn missing_fields_default_to_zero_offset() {
        let dir = TempDir::new().unwrap();
        std::fs::create_dir_all(user_state_dir(dir.path())).unwrap();
        std::fs::write(
            subscriptions_path(dir.path()),
            r#"{"subscriptions":{"deadbeef":{}}}"#,
        )
        .unwrap();
        let got = load(dir.path()).unwrap();
        let s = got.subscriptions.get("deadbeef").unwrap();
        assert_eq!(s.offset_seq, 0);
        assert_eq!(s.topic_name, "");
        assert!(s.filter.is_none());
    }

    #[test]
    fn empty_file_loads_as_empty() {
        let dir = TempDir::new().unwrap();
        std::fs::create_dir_all(user_state_dir(dir.path())).unwrap();
        std::fs::write(subscriptions_path(dir.path()), b"").unwrap();
        let got = load(dir.path()).unwrap();
        assert!(got.subscriptions.is_empty());
    }
}
