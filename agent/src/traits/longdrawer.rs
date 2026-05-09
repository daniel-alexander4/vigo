// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports per-user longdrawer mesh state.
//!
//! The longdrawer reactor writes `UserState { files, tombstones }` per
//! user at `/var/lib/vigo/longdrawer/<user>.json` on each reconcile.
//! This collector reads every state file on the envoy and ships it
//! as the `longdrawer` trait — no separate snapshot writer needed
//! because longdrawer already persists what the UI wants to see.
//!
//! Returns `Value::Null` when no per-user state files exist (cold
//! envoy that never had a `~/longdrawer/` directory); matches the
//! pattern used by other observability-only trait collectors.

use std::collections::BTreeMap;
use std::fs;

use anyhow::Result;
use serde::{Deserialize, Serialize};
use serde_json::{to_value, Value};
use sha2::Digest;

use crate::platform::paths::default_longdrawer_dir;
use crate::swarm::longdrawer::state::UserState;

/// One file observed in a user's `~/longdrawer/`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FileEntry {
    pub name: String,
    pub size: u64,
    pub mtime: i64,
    pub sha256: String,
}

/// Tombstone observation for a user's `~/longdrawer/`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TombstoneEntry {
    pub name: String,
    pub deletion_time: i64,
}

/// Cap on inline recent files. Same ceiling as lockbox's snapshot
/// cap — keeps announce payloads bounded at scale.
pub const RECENT_FILE_CAP: usize = 20;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UserSnapshot {
    pub user: String,
    pub file_count: usize,
    pub total_bytes: u64,
    pub files: Vec<FileEntry>,
    pub files_digest: String,
    pub tombstone_count: usize,
    pub tombstones: Vec<TombstoneEntry>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TraitPayload {
    pub updated_at: i64,
    pub users: Vec<UserSnapshot>,
}

pub fn collect() -> Result<Value> {
    let dir = default_longdrawer_dir();
    if !dir.is_dir() {
        return Ok(Value::Null);
    }

    let mut users: Vec<UserSnapshot> = Vec::new();
    for entry in match fs::read_dir(&dir) {
        Ok(rd) => rd,
        Err(_) => return Ok(Value::Null),
    } {
        let entry = match entry {
            Ok(e) => e,
            Err(_) => continue,
        };
        let name = match entry.file_name().to_str().map(|s| s.to_string()) {
            Some(n) => n,
            None => continue,
        };
        let user = match name.strip_suffix(".json") {
            Some(u) => u.to_string(),
            None => continue,
        };
        // `UserState::load` is forgiving — returns empty on
        // read/parse failure. That's fine for observability; we
        // don't want one bad state file to blank the whole trait.
        let state = UserState::load(&dir, &user);
        users.push(user_snapshot(state));
    }
    users.sort_by(|a, b| a.user.cmp(&b.user));

    if users.is_empty() {
        return Ok(Value::Null);
    }

    let payload = TraitPayload {
        updated_at: now_secs(),
        users,
    };
    Ok(to_value(payload)?)
}

fn user_snapshot(state: UserState) -> UserSnapshot {
    let files_full: Vec<FileEntry> = state
        .files
        .iter()
        .map(|(name, e)| FileEntry {
            name: name.clone(),
            size: e.size,
            mtime: e.mtime,
            sha256: e.sha256.clone(),
        })
        .collect();
    let file_count = files_full.len();
    let total_bytes: u64 = files_full.iter().map(|f| f.size).sum();
    let files_digest = digest_files(&files_full);

    let mut sorted = files_full;
    sorted.sort_by(|a, b| b.mtime.cmp(&a.mtime).then_with(|| a.name.cmp(&b.name)));
    let recent: Vec<FileEntry> = sorted.into_iter().take(RECENT_FILE_CAP).collect();

    let tombstones_all: BTreeMap<String, i64> = state
        .tombstones
        .iter()
        .map(|(name, t)| (name.clone(), t.deletion_time))
        .collect();
    let tombstone_count = tombstones_all.len();
    let mut tombstones: Vec<TombstoneEntry> = tombstones_all
        .into_iter()
        .map(|(name, deletion_time)| TombstoneEntry {
            name,
            deletion_time,
        })
        .collect();
    tombstones.sort_by(|a, b| b.deletion_time.cmp(&a.deletion_time));

    UserSnapshot {
        user: state.user,
        file_count,
        total_bytes,
        files: recent,
        files_digest,
        tombstone_count,
        tombstones,
    }
}

fn digest_files(files: &[FileEntry]) -> String {
    let mut names: Vec<&FileEntry> = files.iter().collect();
    names.sort_by(|a, b| a.name.cmp(&b.name));
    let mut h = sha2::Sha256::new();
    for f in names {
        h.update(f.name.as_bytes());
        h.update(b"|");
        h.update(f.sha256.as_bytes());
        h.update(b"|");
        h.update(f.size.to_be_bytes());
        h.update(b"\n");
    }
    hex::encode(&h.finalize()[..16])
}

fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}
