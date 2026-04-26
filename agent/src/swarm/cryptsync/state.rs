// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Cryptsync on-wire + on-disk state.
//!
//! - **Announcement** — the UDP multicast / cross-subnet unicast
//!   payload. Carries one envoy's pubkey for one user. Unlike
//!   filesync's announcement (which piggybacks full file + tombstone
//!   state), cryptsync's is deliberately tiny: push/pull are
//!   event-driven, not inferred from announcements.
//!
//! - **PeerEntry** — one known peer's view of itself: hostname,
//!   pubkey, last-seen timestamp. Stored in `peers.json` keyed by
//!   hostname.
//!
//! - **PeerTable** — the whole per-user peer directory, persisted to
//!   `/var/lib/vigo/swarm/cryptsync/<user>/peers.json` (root-owned, 0600).

use std::collections::BTreeMap;
use std::fs;
use std::io::Write;

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};

use super::config;

/// Fresh-peer age-out threshold: a peer that hasn't announced in more
/// than this many seconds is dropped from `recipients.txt` (passive
/// revocation). Matches 3× the announce interval so two missed
/// broadcasts don't cause a flap.
pub const STALE_SECS: i64 = 90;

/// One cryptsync peer as observed by this envoy.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct PeerEntry {
    /// Peer's reported hostname. Also the BTreeMap key in PeerTable.
    pub hostname: String,
    /// age x25519 public key in `age1...` bech32 form.
    pub pubkey: String,
    /// Ed25519 signing pubkey in `ed25519:<hex>` form, used to verify
    /// DELETE envelopes originating from this peer. Empty string when
    /// the peer was discovered by a pre-0.27.4 announcement that didn't
    /// carry a signing pubkey; destructive operations from such peers
    /// are rejected.
    #[serde(default)]
    pub sign_pubkey: String,
    /// Last-seen unix seconds (from `emitted_at` on the most recent
    /// announcement we received from this peer).
    pub last_seen: i64,
    /// Socket address the peer last announced from. Populated from
    /// the UDP multicast packet source or the `/cryptsync/announce`
    /// unicast sender's TCP peer address. Required for the pusher —
    /// `peers.json` records hostnames, but the push HTTP client
    /// needs an IP to connect to. `None` if the peer is known only
    /// through recipients.txt (e.g. pre-populated manually).
    #[serde(default)]
    pub last_seen_addr: Option<String>,
}

/// Per-user peer table. Persisted at
/// `/var/lib/vigo/swarm/cryptsync/<user>/peers.json`.
///
/// Does NOT include this envoy itself — the local envoy's own pubkey
/// is folded into `recipients.txt` separately from the peer table.
/// This keeps `peers.json` a pure remote-peer view, which simplifies
/// "who am I hearing from?" debugging.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct PeerTable {
    pub user: String,
    pub peers: BTreeMap<String, PeerEntry>,
}

impl PeerTable {
    pub fn empty(user: &str) -> Self {
        Self {
            user: user.to_string(),
            peers: BTreeMap::new(),
        }
    }

    /// Load from disk, or return empty if the file is missing or
    /// malformed. Never errors — absence is the common case on first
    /// boot.
    pub fn load(user: &str) -> Self {
        let path = config::peers_path(user);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_else(|e| {
                tracing::warn!(
                    path = %path.display(),
                    error = %e,
                    "cryptsync: malformed peers.json, starting fresh"
                );
                Self::empty(user)
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// Atomic save via temp + rename. Root-owned, 0600.
    pub fn save(&self) -> Result<()> {
        let dir = config::agent_state_dir(&self.user);
        fs::create_dir_all(&dir).with_context(|| format!("creating {}", dir.display()))?;
        let path = config::peers_path(&self.user);
        let tmp = path.with_extension("json.tmp");
        let json = serde_json::to_string_pretty(self).context("serializing peer table")?;

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
        Ok(())
    }

    /// Atomic load → mutate → save under the per-user peers lock.
    /// Use this for any read-modify-write (upsert from announce,
    /// forget, prune_stale) — it prevents the lost-update race
    /// where two concurrent callers both load the same starting
    /// state and the second save() overwrites the first. Closure
    /// returns `true` if it mutated the table; the helper only
    /// saves when so.
    pub fn modify<F>(user: &str, f: F) -> Result<()>
    where
        F: FnOnce(&mut Self) -> bool,
    {
        let lock = super::locks::user_lock("peers", user);
        let _guard = lock.lock().unwrap_or_else(|p| p.into_inner());
        let mut table = Self::load(user);
        if f(&mut table) {
            table.save()?;
        }
        Ok(())
    }

    /// Insert-or-update. Returns true if this produced a real change
    /// (new peer, pubkey rotation, or last_seen advanced meaningfully)
    /// — the caller can skip `save()` + `regenerate_recipients()` when
    /// the update is a plain heartbeat.
    pub fn upsert(&mut self, entry: PeerEntry) -> bool {
        match self.peers.get(&entry.hostname) {
            Some(existing) => {
                if existing.pubkey != entry.pubkey || existing.sign_pubkey != entry.sign_pubkey {
                    // Pubkey rotation (age or sign) — real change; future
                    // work adds rotation validation. Accept silently.
                    self.peers.insert(entry.hostname.clone(), entry);
                    true
                } else if entry.last_seen > existing.last_seen {
                    // Heartbeat update — we want this persisted so
                    // stale-peer age-out works, but it's not a
                    // recipient-set change. Also refresh addr so the
                    // pusher has current routing info.
                    let mut updated = existing.clone();
                    updated.last_seen = entry.last_seen;
                    if entry.last_seen_addr.is_some() {
                        updated.last_seen_addr = entry.last_seen_addr;
                    }
                    self.peers.insert(entry.hostname.clone(), updated);
                    false
                } else {
                    false
                }
            }
            None => {
                self.peers.insert(entry.hostname.clone(), entry);
                true
            }
        }
    }

    /// Look up a peer's signing pubkey by hostname. Returns `None`
    /// when the peer isn't known, or when the peer is known but
    /// carries no signing pubkey (pre-0.27.4 peer that didn't
    /// advertise one).
    pub fn sign_pubkey_for(&self, hostname: &str) -> Option<&str> {
        self.peers
            .get(hostname)
            .map(|p| p.sign_pubkey.as_str())
            .filter(|s| !s.is_empty())
    }

    /// Remove a peer by hostname. Returns true if it was present.
    pub fn forget(&mut self, hostname: &str) -> bool {
        self.peers.remove(hostname).is_some()
    }

    /// Remove a peer by age pubkey. Used when applying a LEAVE envelope,
    /// where the authoritative key is the subject's age pubkey (a
    /// re-enrolled host under the same hostname has a different pubkey,
    /// so keying by pubkey prevents replays from affecting the reborn
    /// host). Returns true if a matching peer was removed.
    pub fn forget_by_pubkey(&mut self, age_pubkey: &str) -> bool {
        let key = self
            .peers
            .iter()
            .find(|(_, p)| p.pubkey == age_pubkey)
            .map(|(k, _)| k.clone());
        match key {
            Some(k) => {
                self.peers.remove(&k);
                true
            }
            None => false,
        }
    }

    /// Drop peers whose `last_seen` is older than `now - STALE_SECS`.
    /// Returns true if any peer was dropped (caller should regenerate
    /// recipients.txt when so).
    pub fn prune_stale(&mut self, now: i64) -> bool {
        let before = self.peers.len();
        self.peers.retain(|_, p| now - p.last_seen <= STALE_SECS);
        self.peers.len() != before
    }

    /// Current list of non-stale pubkeys, sorted for deterministic
    /// recipients.txt output.
    pub fn live_pubkeys(&self, now: i64) -> Vec<String> {
        let mut keys: Vec<String> = self
            .peers
            .values()
            .filter(|p| now - p.last_seen <= STALE_SECS)
            .map(|p| p.pubkey.clone())
            .collect();
        keys.sort();
        keys.dedup();
        keys
    }
}

/// UDP announcement payload. Line-delimited wire format:
/// `CRYPTSYNC-1\n<json>`.
///
/// `generation` is a monotonic counter per (envoy, user) incremented
/// on each boot / identity rotation. Reserved for future key-rotation
/// validation; current code doesn't use it beyond filesyncing it back.
///
/// `sign_pubkey` was added in 0.27.4 alongside tombstone propagation.
/// It's the sender's Ed25519 verification pubkey for DELETE envelopes.
/// Receivers that see an empty `sign_pubkey` record the peer normally
/// but reject destructive operations from it (graceful degrade for
/// pre-0.27.4 peers in the middle of a rolling upgrade).
///
/// `tombstones_digest` is a 16-byte truncated SHA-256 over this
/// sender's per-user tombstone set (see `tombstones::TombstoneTable::digest`).
/// Receivers compare it against their own local digest and issue
/// a `GET /cryptsync/tombstones/<user>` to this sender when they
/// differ — cheap way to discover mesh-wide tombstone divergence
/// without putting the whole list in every announcement.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct Announcement {
    pub machine: String,
    pub user: String,
    pub pubkey: String,
    #[serde(default)]
    pub sign_pubkey: String,
    pub emitted_at: i64,
    pub port: u16,
    #[serde(default)]
    pub generation: u64,
    /// Hex-encoded 16-byte digest. Empty string from pre-0.27.4
    /// peers; receivers treat empty as "skip tombstone catch-up
    /// for this peer."
    #[serde(default)]
    pub tombstones_digest: String,
    /// Hex-encoded 16-byte digest of the per-user `LeavesTable`
    /// (`(age_pubkey, leave_time)` pairs). Mirrors `tombstones_digest`
    /// so an offline-during-LEAVE peer that comes back observes the
    /// historical leave envelopes via catch-up rather than just seeing
    /// the leaver vanish from announcements. Empty string from
    /// pre-0.30.24 peers; receivers treat empty as "skip leaves
    /// catch-up for this peer."
    #[serde(default)]
    pub leaves_digest: String,
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample_peer(name: &str, ts: i64) -> PeerEntry {
        PeerEntry {
            hostname: name.to_string(),
            pubkey: format!("age1{:0>52}", name),
            sign_pubkey: String::new(),
            last_seen: ts,
            last_seen_addr: None,
        }
    }

    #[test]
    fn upsert_new_peer_is_a_real_change() {
        let mut t = PeerTable::empty("dan");
        assert!(t.upsert(sample_peer("host-a", 100)));
    }

    #[test]
    fn upsert_heartbeat_is_not_a_real_change() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("host-a", 100));
        // Same pubkey, newer last_seen — heartbeat only.
        assert!(!t.upsert(sample_peer("host-a", 130)));
        assert_eq!(t.peers.get("host-a").unwrap().last_seen, 130);
    }

    #[test]
    fn upsert_pubkey_rotation_is_a_real_change() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("host-a", 100));
        let rotated = PeerEntry {
            hostname: "host-a".into(),
            pubkey: "age1differentkey".into(),
            sign_pubkey: String::new(),
            last_seen: 150,
            last_seen_addr: None,
        };
        assert!(t.upsert(rotated));
    }

    #[test]
    fn upsert_older_heartbeat_is_ignored() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("host-a", 200));
        // Older announcement — reordering on the wire; don't regress.
        assert!(!t.upsert(sample_peer("host-a", 100)));
        assert_eq!(t.peers.get("host-a").unwrap().last_seen, 200);
    }

    #[test]
    fn prune_stale_drops_old_peers() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("fresh", 200));
        t.upsert(sample_peer("stale", 100));
        // now=250: fresh age=50 (<STALE_SECS=90 → survives),
        //          stale age=150 (>STALE_SECS → dropped).
        let now = 250;
        assert!(t.prune_stale(now));
        assert!(t.peers.contains_key("fresh"));
        assert!(!t.peers.contains_key("stale"));
    }

    #[test]
    fn prune_stale_returns_false_when_nothing_dropped() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("fresh-a", 200));
        t.upsert(sample_peer("fresh-b", 210));
        assert!(!t.prune_stale(250));
    }

    #[test]
    fn live_pubkeys_excludes_stale_and_sorts() {
        let mut t = PeerTable::empty("dan");
        t.upsert(PeerEntry {
            hostname: "c".into(),
            pubkey: "age1c".into(),
            sign_pubkey: String::new(),
            last_seen: 100,
            last_seen_addr: None,
        });
        t.upsert(PeerEntry {
            hostname: "a".into(),
            pubkey: "age1a".into(),
            sign_pubkey: String::new(),
            last_seen: 200,
            last_seen_addr: None,
        });
        t.upsert(PeerEntry {
            hostname: "stale".into(),
            pubkey: "age1stale".into(),
            sign_pubkey: String::new(),
            last_seen: 10,
            last_seen_addr: None,
        });
        let keys = t.live_pubkeys(200);
        // "c" has last_seen=100; now-last_seen=100 > STALE_SECS=90 → stale.
        // "stale" is obviously stale. "a" is fresh.
        assert_eq!(keys, vec!["age1a".to_string()]);
    }

    #[test]
    fn forget_removes_peer() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("host-a", 100));
        assert!(t.forget("host-a"));
        assert!(!t.forget("host-a")); // second call is a no-op
    }

    #[test]
    fn announcement_json_roundtrips() {
        let a = Announcement {
            machine: "laptop".into(),
            user: "dan".into(),
            pubkey: "age1abc".into(),
            sign_pubkey: String::new(),
            emitted_at: 1_700_000_000,
            port: 1531,
            generation: 7,
            tombstones_digest: String::new(),
            leaves_digest: String::new(),
        };
        let bytes = serde_json::to_vec(&a).unwrap();
        let parsed: Announcement = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(a, parsed);
    }
}
