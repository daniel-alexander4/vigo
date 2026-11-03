// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Lockbox ever-known puddle members — the durable membership
//! denominator behind the `recipients:` status line.
//!
//! ## What problem this solves
//!
//! `peers.json` is the *liveness* table: peers age out when their
//! announces stop landing within `STALE_SECS`. That makes it the
//! right input for `recipients.txt` regen but the wrong input for
//! "how many envoys are in this puddle?" — once `prune_stale` drops
//! a long-absent peer, the live members count shrinks in lockstep
//! with the recipient count and the `lockbox status` gap line goes
//! silent. Persistent absence then renders identical to steady
//! state, which is exactly the case where an operator most needs a
//! warning.
//!
//! `puddle_members.json` is the *membership* table: append-only,
//! mutated only by authoritative removal (LEAVE envelope or admin
//! `forget`). Every successful peer upsert whose announced
//! `puddle_pubkey` matches the local user's puddle pubkey lands a
//! record here, keyed by age pubkey (matches the LEAVE convention
//! so a re-enrolled host under the same hostname is a fresh member,
//! not a re-asserted one). `last_seen` is updated on every
//! sighting; nothing else shrinks the set besides `apply_leave` and
//! per-host `forget`.
//!
//! Status-line consumers compare entries here against the live
//! recipients to render real silent-for durations even after
//! `prune_stale` has dropped the peer from `peers.json` — the
//! denominator and the duration both keep working hours and days
//! into a peer's absence.
//!
//! Persistence: `/var/lib/vigo/swarm/lockbox/<username>/puddle_members.json`
//! (root-owned, 0600) — canonical, daemon-only. After every save
//! the daemon also writes a user-readable mirror at
//! `~<user>/.lockbox/puddle_members.json` (user-owned, 0600) so
//! `vigo swarm lockbox status` can read it without sudo. Mirrors
//! the tombstones / leaves / peers shape exactly.

use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::path::PathBuf;

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};

use super::config;

/// One puddle member — an age pubkey that has been observed
/// announcing under the local user's puddle pubkey at least once.
///
/// Hostname is the last-known hostname for UI / log rendering only;
/// the table is keyed on age pubkey (immutable, unforgeable, matches
/// the LEAVE record subject). first_seen is set on insertion and
/// never advances; last_seen tracks the most recent sighting and
/// drives the silent-for duration in `lockbox status`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct PuddleMemberRecord {
    pub hostname: String,
    pub first_seen: i64,
    pub last_seen: i64,
}

/// Per-user puddle-membership table. Persisted at
/// `/var/lib/vigo/swarm/lockbox/<user>/puddle_members.json`.
///
/// Self is **not** in this table — the local envoy's own age pubkey
/// is added to `recipients.txt` separately and the status-line
/// derivation adds 1 for self when computing `members_count`.
/// Matches the leaves / tombstones convention of "remote-peer view
/// only."
///
/// BTreeMap rather than HashMap so the serialized form is
/// deterministic across envoys that built the same logical set in
/// different insertion orders — useful for diffing snapshot dumps
/// during livetests.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct PuddleMembersTable {
    pub user: String,
    pub members: BTreeMap<String, PuddleMemberRecord>,
}

impl PuddleMembersTable {
    pub fn empty(user: &str) -> Self {
        Self {
            user: user.to_string(),
            members: BTreeMap::new(),
        }
    }

    /// Load from disk, or return empty if the file is missing or
    /// malformed. Never errors — absence is the common case on first
    /// boot (the file is created lazily on first matching upsert).
    /// Malformed content is logged, discarded, and the empty
    /// replacement is written back so subsequent loads stop
    /// re-parsing the same broken file.
    pub fn load(user: &str) -> Self {
        let path = puddle_members_path(user);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_else(|e| {
                tracing::warn!(
                    path = %path.display(),
                    error = %e,
                    "lockbox: malformed puddle_members.json, starting fresh and overwriting"
                );
                let empty = Self::empty(user);
                if let Err(save_err) = empty.save() {
                    tracing::warn!(
                        path = %path.display(),
                        error = %save_err,
                        "lockbox: failed to overwrite malformed puddle_members.json; will re-warn next load"
                    );
                }
                empty
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// CLI-facing load that reads the user-side mirror at
    /// `~/.lockbox/puddle_members.json` instead of the daemon-only
    /// canonical path. Used by `vigo swarm lockbox status` running
    /// as the unprivileged user. Same fall-through-to-empty
    /// semantics as `load`.
    pub fn load_user(user: &str, home: &std::path::Path) -> Self {
        let path = config::puddle_members_user_path(home);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_else(|e| {
                tracing::warn!(
                    path = %path.display(),
                    error = %e,
                    "lockbox: malformed user-mirror puddle_members.json, starting fresh"
                );
                Self::empty(user)
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// Atomic save via temp + rename. Canonical state is root-owned
    /// 0600 at `agent_state_dir/<user>/puddle_members.json`. After
    /// the canonical write succeeds, a best-effort mirror is also
    /// written at `~<user>/.lockbox/puddle_members.json` (user-
    /// owned, 0600) so user-context CLI verbs can read it without
    /// sudo.
    pub fn save(&self) -> Result<()> {
        let dir = config::agent_state_dir(&self.user);
        fs::create_dir_all(&dir).with_context(|| format!("creating {}", dir.display()))?;
        let path = puddle_members_path(&self.user);
        let tmp = path.with_extension("json.tmp");
        let json = serde_json::to_string_pretty(self).context("serializing puddle members table")?;

        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            let mut f = fs::OpenOptions::new()
                .write(true)
                .create(true)
                .truncate(true)
                .mode(0o600)
                .open(&tmp)
                .with_context(|| format!("creating {}", tmp.display()))?;
            f.write_all(json.as_bytes())?;
            f.sync_all().ok();
        }
        #[cfg(not(unix))]
        {
            fs::write(&tmp, json.as_bytes())
                .with_context(|| format!("writing {}", tmp.display()))?;
        }

        fs::rename(&tmp, &path).with_context(|| format!("renaming to {}", path.display()))?;

        // User-side mirror — best-effort, see write_user_mirror_json.
        super::write_user_mirror_json(&self.user, "puddle_members.json", &json);
        Ok(())
    }

    /// Atomic load → mutate → save under the per-user puddle_members
    /// lock. Use this for any read-modify-write (upsert from
    /// announce, eviction from apply_leave) — it prevents the
    /// lost-update race where two concurrent callers both load the
    /// same starting state and the second save() overwrites the
    /// first. The closure returns `true` if it mutated the table;
    /// the helper only saves when so.
    pub fn modify<F>(user: &str, f: F) -> Result<()>
    where
        F: FnOnce(&mut Self) -> bool,
    {
        let lock = super::locks::user_lock(super::locks::LockTable::PuddleMembers, user);
        let _guard = lock.lock().unwrap_or_else(|p| p.into_inner());
        let mut table = Self::load(user);
        if f(&mut table) {
            table.save()?;
        }
        Ok(())
    }

    /// Insert-or-update a member. On first sighting of `age_pubkey`,
    /// records `{hostname, first_seen=now, last_seen=now}`. On
    /// subsequent sightings, only `last_seen` (and `hostname` if it
    /// drifted) is updated; `first_seen` is preserved. Returns true
    /// if the call mutated the stored record (new entry, hostname
    /// drift, or last_seen advanced).
    pub fn upsert(&mut self, age_pubkey: &str, hostname: &str, now: i64) -> bool {
        match self.members.get(age_pubkey) {
            Some(existing) => {
                let mut changed = false;
                let mut updated = existing.clone();
                if existing.hostname != hostname {
                    updated.hostname = hostname.to_string();
                    changed = true;
                }
                if now > existing.last_seen {
                    updated.last_seen = now;
                    changed = true;
                }
                if changed {
                    self.members.insert(age_pubkey.to_string(), updated);
                }
                changed
            }
            None => {
                self.members.insert(
                    age_pubkey.to_string(),
                    PuddleMemberRecord {
                        hostname: hostname.to_string(),
                        first_seen: now,
                        last_seen: now,
                    },
                );
                true
            }
        }
    }

    /// Drop a member by age pubkey. Authoritative removal — invoked
    /// from `apply_leave` (any code path that lands a LEAVE) and
    /// from admin `forget`. Returns true if the entry was present.
    pub fn forget_by_pubkey(&mut self, age_pubkey: &str) -> bool {
        self.members.remove(age_pubkey).is_some()
    }

    /// Seed from a peers snapshot when this table is empty. Called
    /// from the announce-receive path on first 0.39.12 startup
    /// against an existing puddle: peers.json already carries
    /// every member's `puddle_pubkey` via TOFU pinning; we adopt
    /// those entries so the gap line works from the first tick
    /// rather than waiting on every peer to re-announce. No-op
    /// when the table already has entries (subsequent boots) or
    /// when no matching peers exist (genuinely-fresh puddle).
    pub fn seed_from_peers_if_empty(
        &mut self,
        peers: &super::state::PeerTable,
        local_puddle_pubkey: &str,
    ) -> bool {
        if !self.members.is_empty() {
            return false;
        }
        if local_puddle_pubkey.is_empty() {
            return false;
        }
        let mut changed = false;
        for (host, peer) in &peers.peers {
            if peer.puddle_pubkey == local_puddle_pubkey {
                self.members.insert(
                    peer.pubkey.clone(),
                    PuddleMemberRecord {
                        hostname: host.clone(),
                        first_seen: peer.last_seen,
                        last_seen: peer.last_seen,
                    },
                );
                changed = true;
            }
        }
        changed
    }
}

/// `/var/lib/vigo/swarm/lockbox/<user>/puddle_members.json`.
pub fn puddle_members_path(username: &str) -> PathBuf {
    config::agent_state_dir(username).join("puddle_members.json")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rec(host: &str, first: i64, last: i64) -> PuddleMemberRecord {
        PuddleMemberRecord {
            hostname: host.to_string(),
            first_seen: first,
            last_seen: last,
        }
    }

    #[test]
    fn upsert_first_sighting_records_both_seen_times() {
        let mut t = PuddleMembersTable::empty("dan");
        assert!(t.upsert("age1abc", "annlap", 1000));
        let r = t.members.get("age1abc").unwrap();
        assert_eq!(r.hostname, "annlap");
        assert_eq!(r.first_seen, 1000);
        assert_eq!(r.last_seen, 1000);
    }

    #[test]
    fn upsert_subsequent_advances_last_seen_only() {
        let mut t = PuddleMembersTable::empty("dan");
        t.upsert("age1abc", "annlap", 1000);
        assert!(t.upsert("age1abc", "annlap", 2000));
        let r = t.members.get("age1abc").unwrap();
        assert_eq!(r.first_seen, 1000); // preserved
        assert_eq!(r.last_seen, 2000);
    }

    #[test]
    fn upsert_older_last_seen_is_noop() {
        let mut t = PuddleMembersTable::empty("dan");
        t.upsert("age1abc", "annlap", 2000);
        // Older sighting (clock skew, reorder) should not regress.
        assert!(!t.upsert("age1abc", "annlap", 1000));
        assert_eq!(t.members.get("age1abc").unwrap().last_seen, 2000);
    }

    #[test]
    fn upsert_hostname_drift_records_new_name() {
        // A peer is renamed (e.g., laptop hostname changed). The age
        // pubkey is unchanged; the table follows the rename.
        let mut t = PuddleMembersTable::empty("dan");
        t.upsert("age1abc", "old-name", 1000);
        assert!(t.upsert("age1abc", "new-name", 1500));
        assert_eq!(t.members.get("age1abc").unwrap().hostname, "new-name");
    }

    #[test]
    fn forget_by_pubkey_removes_member() {
        let mut t = PuddleMembersTable::empty("dan");
        t.upsert("age1abc", "annlap", 1000);
        t.upsert("age1def", "plex", 1000);
        assert!(t.forget_by_pubkey("age1abc"));
        assert!(!t.members.contains_key("age1abc"));
        assert!(t.members.contains_key("age1def"));
    }

    #[test]
    fn forget_by_pubkey_unknown_returns_false() {
        let mut t = PuddleMembersTable::empty("dan");
        t.upsert("age1abc", "annlap", 1000);
        assert!(!t.forget_by_pubkey("age1unknown"));
    }

    #[test]
    fn seed_from_peers_populates_when_empty() {
        let mut t = PuddleMembersTable::empty("dan");
        let mut peers = super::super::state::PeerTable::empty("dan");
        peers.peers.insert(
            "annlap".to_string(),
            super::super::state::PeerEntry {
                hostname: "annlap".to_string(),
                pubkey: "age1abc".to_string(),
                sign_pubkey: "ed25519:00".to_string(),
                last_seen: 1234,
                last_seen_addr: None,
                puddle_pubkey: "puddle-x".to_string(),
                pinned_puddle_pubkey: "puddle-x".to_string(),
            },
        );
        peers.peers.insert(
            "stranger".to_string(),
            super::super::state::PeerEntry {
                hostname: "stranger".to_string(),
                pubkey: "age1xyz".to_string(),
                sign_pubkey: "ed25519:00".to_string(),
                last_seen: 1234,
                last_seen_addr: None,
                puddle_pubkey: "puddle-other".to_string(),
                pinned_puddle_pubkey: "puddle-other".to_string(),
            },
        );
        assert!(t.seed_from_peers_if_empty(&peers, "puddle-x"));
        // Only the puddle-x match landed in the table.
        assert_eq!(t.members.len(), 1);
        assert!(t.members.contains_key("age1abc"));
    }

    #[test]
    fn seed_from_peers_skips_when_already_populated() {
        let mut t = PuddleMembersTable::empty("dan");
        t.members.insert("age1abc".to_string(), rec("annlap", 100, 200));
        let mut peers = super::super::state::PeerTable::empty("dan");
        peers.peers.insert(
            "later-peer".to_string(),
            super::super::state::PeerEntry {
                hostname: "later-peer".to_string(),
                pubkey: "age1other".to_string(),
                sign_pubkey: "ed25519:00".to_string(),
                last_seen: 5000,
                last_seen_addr: None,
                puddle_pubkey: "puddle-x".to_string(),
                pinned_puddle_pubkey: "puddle-x".to_string(),
            },
        );
        assert!(!t.seed_from_peers_if_empty(&peers, "puddle-x"));
        // Pre-existing entry untouched; later-peer NOT adopted (would
        // be a re-seed that double-counts).
        assert_eq!(t.members.len(), 1);
        assert!(t.members.contains_key("age1abc"));
    }

    #[test]
    fn seed_from_peers_skips_when_no_local_puddle() {
        // Standalone mode (empty local pubkey) → no seeding, no
        // table population. The bare `recipients: N` line stays
        // unchanged from pre-0.39.12.
        let mut t = PuddleMembersTable::empty("dan");
        let mut peers = super::super::state::PeerTable::empty("dan");
        peers.peers.insert(
            "annlap".to_string(),
            super::super::state::PeerEntry {
                hostname: "annlap".to_string(),
                pubkey: "age1abc".to_string(),
                sign_pubkey: "ed25519:00".to_string(),
                last_seen: 1234,
                last_seen_addr: None,
                puddle_pubkey: "puddle-x".to_string(),
                pinned_puddle_pubkey: "puddle-x".to_string(),
            },
        );
        assert!(!t.seed_from_peers_if_empty(&peers, ""));
        assert!(t.members.is_empty());
    }

    #[test]
    fn save_load_round_trip() {
        // Tempdir not directly usable here because save() writes to
        // `/var/lib/vigo/...`. Test the in-memory shape via JSON
        // round-trip instead — same serde path as save/load.
        let mut t = PuddleMembersTable::empty("dan");
        t.upsert("age1abc", "annlap", 1000);
        t.upsert("age1def", "plex", 2000);
        let json = serde_json::to_string(&t).unwrap();
        let back: PuddleMembersTable = serde_json::from_str(&json).unwrap();
        assert_eq!(back.user, "dan");
        assert_eq!(back.members.len(), 2);
        assert_eq!(back.members.get("age1abc").unwrap().hostname, "annlap");
        assert_eq!(back.members.get("age1def").unwrap().last_seen, 2000);
    }
}
