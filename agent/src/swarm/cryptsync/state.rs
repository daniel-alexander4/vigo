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
    /// Hex-encoded self-fleet pubkey the peer announced (ADR-016).
    /// Empty when the peer is running standalone (self-fleet not
    /// initialized) or the announcement predates 0.35.x. Recipient
    /// resolution under fleet mode filters peers whose `fleet_pubkey`
    /// doesn't match the local user's self-fleet — the gossip layer
    /// is fleet-aware while age keys stay per-envoy.
    #[serde(default)]
    pub fleet_pubkey: String,
    /// Trust-on-first-use pinned fleet pubkey (ADR-016 follow-up).
    /// Set on first sighting from `fleet_pubkey`; subsequent
    /// announces under a different fleet_pubkey are rejected at
    /// upsert time. Empty when the peer was first seen running
    /// standalone — once a fleet pubkey is pinned, we never let the
    /// peer "revert" without an admin clearing the entry. Mirrors
    /// `gitmirror::peer_pubkey_cache`'s TOFU shape, embedded
    /// per-peer here because cryptsync's PeerTable is already
    /// per-user and that's the right scope for cryptsync TOFU.
    #[serde(default)]
    pub pinned_fleet_pubkey: String,
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
    ///
    /// ADR-016 follow-up: rejects announcements whose `fleet_pubkey`
    /// disagrees with the previously-pinned value for the same
    /// hostname. Returns `false` (no mutation) and the receive-side
    /// caller logs the rejection. Operator recovery is to clear the
    /// peer entry — `vigo swarm cryptsync forget <host>` does the
    /// trick. First sighting under a non-empty fleet_pubkey pins it
    /// to `pinned_fleet_pubkey` for future TOFU checks.
    pub fn upsert(&mut self, mut entry: PeerEntry) -> bool {
        match self.peers.get(&entry.hostname) {
            Some(existing) => {
                // TOFU check — once we've pinned a non-empty fleet
                // pubkey, the peer must keep announcing the same one.
                // An empty incoming under a non-empty pin is rejected
                // (peer "reverted to standalone" looks identical on
                // the wire to a same-LAN spoofer, so be strict).
                if !existing.pinned_fleet_pubkey.is_empty()
                    && existing.pinned_fleet_pubkey != entry.fleet_pubkey
                {
                    // Mismatch — drop the announce. Receive-side
                    // caller logs the warn message with full detail.
                    return false;
                }
                if existing.pubkey != entry.pubkey || existing.sign_pubkey != entry.sign_pubkey {
                    // Pubkey rotation (age or sign) — real change.
                    // Carry the pin forward: it's already empty (and
                    // entry.fleet_pubkey may legitimately set it for
                    // the first time) or it's non-empty and matches
                    // entry.fleet_pubkey (verified above).
                    entry.pinned_fleet_pubkey = if entry.fleet_pubkey.is_empty() {
                        existing.pinned_fleet_pubkey.clone()
                    } else {
                        entry.fleet_pubkey.clone()
                    };
                    self.peers.insert(entry.hostname.clone(), entry);
                    true
                } else if entry.last_seen > existing.last_seen {
                    // Heartbeat update — we want this persisted so
                    // stale-peer age-out works, but it's not a
                    // recipient-set change. Also refresh addr so the
                    // pusher has current routing info. Preserve the
                    // existing pin; refresh fleet_pubkey only if the
                    // pin was already empty (legitimate first-set).
                    let mut updated = existing.clone();
                    updated.last_seen = entry.last_seen;
                    if entry.last_seen_addr.is_some() {
                        updated.last_seen_addr = entry.last_seen_addr;
                    }
                    if updated.pinned_fleet_pubkey.is_empty()
                        && !entry.fleet_pubkey.is_empty()
                    {
                        // First-sighting fleet pubkey pin (peer just
                        // adopted self-fleet between heartbeats).
                        updated.fleet_pubkey = entry.fleet_pubkey;
                        updated.pinned_fleet_pubkey = updated.fleet_pubkey.clone();
                    }
                    self.peers.insert(entry.hostname.clone(), updated);
                    false
                } else {
                    false
                }
            }
            None => {
                // First sighting of this hostname. TOFU-pin whatever
                // fleet_pubkey the announce carried (empty stays
                // empty — peer is running standalone).
                if entry.pinned_fleet_pubkey.is_empty() {
                    entry.pinned_fleet_pubkey = entry.fleet_pubkey.clone();
                }
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

    /// Clear `pinned_fleet_pubkey` on every peer whose recorded
    /// `fleet_pubkey` matches `retired`. Returns the number of
    /// entries adjusted. Used by self-fleet rekey (ADR-018) so peers
    /// announcing under the post-rotation fleet pubkey don't trip
    /// the TOFU mismatch check at upsert time. The `fleet_pubkey`
    /// field itself is left alone — `live_pubkeys_for_fleets` will
    /// filter the entry out until the peer's next announce updates
    /// it, which is the correct behavior during a rotation rollout.
    pub fn clear_pins_for_fleet(&mut self, retired: &str) -> usize {
        let mut adjusted = 0usize;
        for entry in self.peers.values_mut() {
            if entry.fleet_pubkey == retired
                && !entry.pinned_fleet_pubkey.is_empty()
            {
                entry.pinned_fleet_pubkey.clear();
                adjusted += 1;
            }
        }
        adjusted
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

    /// Fleet-aware variant of [`live_pubkeys`] (ADR-016). The caller
    /// passes the set of fleet pubkeys whose peers count as recipient
    /// candidates. An empty slice means "no fleet filter" — standalone
    /// mode, identical to [`live_pubkeys`].
    ///
    /// Steady state passes a one-element slice (the local user's
    /// self-fleet pubkey). During an ADR-018 rekey rollout, the founder
    /// passes the new fleet pubkey *and* every unexpired retired fleet
    /// pubkey from `~/.vigo-fleet/retired_fleets.json`, so peers still
    /// announcing under the old fleet during the 24h graceful window
    /// stay in the recipient set rather than silently disappearing
    /// until they finish their own rekey.
    ///
    /// Per-envoy age keys stay deliberately preserved: even when
    /// filtering by fleet membership, the per-peer pubkeys this
    /// returns are each peer's individual age x25519 pubkey, not a
    /// shared fleet decryption key. ADR-016 calls this out as
    /// load-bearing — see `mod.rs::regenerate_recipients` for the
    /// invariant comment.
    pub fn live_pubkeys_for_fleets(
        &self,
        now: i64,
        accepted_fleet_pubkeys: &[String],
    ) -> Vec<String> {
        let mut keys: Vec<String> = self
            .peers
            .values()
            .filter(|p| now - p.last_seen <= STALE_SECS)
            .filter(|p| {
                if accepted_fleet_pubkeys.is_empty() {
                    return true;
                }
                accepted_fleet_pubkeys
                    .iter()
                    .any(|fp| p.fleet_pubkey == *fp)
            })
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
    /// Hex-encoded self-fleet Ed25519 pubkey for the announcing user
    /// (ADR-016). Empty string when the user is running standalone
    /// or the announce predates 0.35.x. Receivers under fleet mode
    /// filter peers whose pubkey here doesn't match the local
    /// user's self-fleet — see `state::PeerTable::live_pubkeys_for_fleets`.
    #[serde(default)]
    pub fleet_pubkey: String,
    /// Optional fleet certificate (ADR-016 follow-up). Closes the
    /// same-LAN first-contact race that TOFU pinning has on its own:
    /// receivers verify the cert's signature against `fleet_pubkey`
    /// before accepting the fleet claim. `None` when the announcer
    /// hasn't issued a cert yet (slice 1 / slice 2 peers, or
    /// `cryptsync fleet renew-cert` not run); the announce still
    /// passes through to receivers under TOFU rules but receivers
    /// will warn-log the absence so operators can tell.
    #[serde(default)]
    pub fleet_cert: Option<super::fleet_cert::FleetCert>,
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
            fleet_pubkey: String::new(),
            pinned_fleet_pubkey: String::new(),
        }
    }

    fn sample_peer_in_fleet(name: &str, ts: i64, fleet: &str) -> PeerEntry {
        PeerEntry {
            hostname: name.to_string(),
            pubkey: format!("age1{:0>52}", name),
            sign_pubkey: String::new(),
            last_seen: ts,
            last_seen_addr: None,
            fleet_pubkey: fleet.to_string(),
            // Pin starts empty — upsert pins on first sighting.
            pinned_fleet_pubkey: String::new(),
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
            fleet_pubkey: String::new(),
            pinned_fleet_pubkey: String::new(),
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
    fn clear_pins_for_fleet_only_touches_matching_peers() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("rotating", 100, "fleet-a"));
        t.upsert(sample_peer_in_fleet("other", 100, "fleet-b"));
        t.upsert(sample_peer("standalone", 100));
        // Sanity: TOFU pinned each fleet peer on first sighting.
        assert_eq!(
            t.peers.get("rotating").unwrap().pinned_fleet_pubkey,
            "fleet-a"
        );
        assert_eq!(t.peers.get("other").unwrap().pinned_fleet_pubkey, "fleet-b");
        assert_eq!(t.peers.get("standalone").unwrap().pinned_fleet_pubkey, "");

        let cleared = t.clear_pins_for_fleet("fleet-a");
        assert_eq!(cleared, 1);
        // Only "rotating" lost its pin; the others are untouched.
        assert_eq!(t.peers.get("rotating").unwrap().pinned_fleet_pubkey, "");
        assert_eq!(t.peers.get("other").unwrap().pinned_fleet_pubkey, "fleet-b");
        assert_eq!(t.peers.get("standalone").unwrap().pinned_fleet_pubkey, "");
        // The fleet_pubkey field itself is left alone — recipient
        // resolution will re-pin via the next announce.
        assert_eq!(t.peers.get("rotating").unwrap().fleet_pubkey, "fleet-a");
    }

    #[test]
    fn clear_pins_for_fleet_returns_zero_when_no_match() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("a", 100, "fleet-a"));
        let cleared = t.clear_pins_for_fleet("fleet-z");
        assert_eq!(cleared, 0);
        assert_eq!(t.peers.get("a").unwrap().pinned_fleet_pubkey, "fleet-a");
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
            fleet_pubkey: String::new(),
            pinned_fleet_pubkey: String::new(),
        });
        t.upsert(PeerEntry {
            hostname: "a".into(),
            pubkey: "age1a".into(),
            sign_pubkey: String::new(),
            last_seen: 200,
            last_seen_addr: None,
            fleet_pubkey: String::new(),
            pinned_fleet_pubkey: String::new(),
        });
        t.upsert(PeerEntry {
            hostname: "stale".into(),
            pubkey: "age1stale".into(),
            sign_pubkey: String::new(),
            last_seen: 10,
            last_seen_addr: None,
            fleet_pubkey: String::new(),
            pinned_fleet_pubkey: String::new(),
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
            fleet_pubkey: String::new(),
            fleet_cert: None,
        };
        let bytes = serde_json::to_vec(&a).unwrap();
        let parsed: Announcement = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(a, parsed);
    }

    /// Pre-0.35.x announcements arrive without `fleet_pubkey`. Receivers
    /// must accept the JSON and treat the field as empty (standalone
    /// peer). Regression guard against the serde-default tag drifting
    /// out of sync with the wire schema.
    #[test]
    fn announcement_json_accepts_pre_fleet_payload() {
        let json = br#"{
            "machine": "old",
            "user": "dan",
            "pubkey": "age1abc",
            "emitted_at": 1700000000,
            "port": 1531
        }"#;
        let parsed: Announcement = serde_json::from_slice(json).unwrap();
        assert_eq!(parsed.machine, "old");
        assert_eq!(parsed.fleet_pubkey, "");
    }

    // ---- ADR-016 fleet-aware recipient resolution ----

    /// Two envoys in the same fleet must each contribute a *distinct*
    /// per-envoy age pubkey to the recipient set. ADR-016 deliberately
    /// preserves per-envoy keys: anyone refactoring fleet resolution
    /// to "use one fleet decryption key" hits this test failing with
    /// a name that documents why.
    #[test]
    fn recipients_are_per_envoy_not_per_fleet() {
        let fleet = "ed25519:8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8";
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("alpha", 200, fleet));
        t.upsert(sample_peer_in_fleet("beta", 200, fleet));

        let keys = t.live_pubkeys_for_fleets(200, &[fleet.to_string()]);

        // Two envoys → two distinct age pubkeys, NOT one fleet key.
        assert_eq!(keys.len(), 2, "expected one age pubkey per envoy");
        assert!(
            keys.iter().all(|k| k.starts_with("age1")),
            "every recipient must be a per-envoy age pubkey, not the fleet pubkey"
        );
        assert!(keys.iter().all(|k| !k.contains(fleet)),
            "the fleet ed25519 pubkey must never appear as an age recipient");
        assert_ne!(keys[0], keys[1], "the two envoys must contribute distinct keys");
    }

    /// Cross-fleet contamination: a peer announcing under a different
    /// fleet pubkey is filtered out. Without this, every envoy on the
    /// LAN would land in everyone else's recipients regardless of
    /// trust scope.
    #[test]
    fn live_pubkeys_for_fleets_filters_other_fleets() {
        let fleet_dan = "fleet-pk-dan";
        let fleet_alice = "fleet-pk-alice";
        let mut t = PeerTable::empty("multi");
        t.upsert(sample_peer_in_fleet("dan-laptop", 200, fleet_dan));
        t.upsert(sample_peer_in_fleet("dan-desktop", 200, fleet_dan));
        t.upsert(sample_peer_in_fleet("alice-laptop", 200, fleet_alice));

        let dan_recipients = t.live_pubkeys_for_fleets(200, &[fleet_dan.to_string()]);
        assert_eq!(dan_recipients.len(), 2);
        assert!(dan_recipients.iter().any(|k| k.contains("dan-laptop")));
        assert!(dan_recipients.iter().any(|k| k.contains("dan-desktop")));
        assert!(
            !dan_recipients.iter().any(|k| k.contains("alice")),
            "alice's envoys must not be recipients of dan's content"
        );
    }

    /// Standalone mode (empty slice) returns every live peer regardless
    /// of `fleet_pubkey` — preserves the legacy gossip-only behavior
    /// for users who haven't opted into self-fleet.
    #[test]
    fn live_pubkeys_for_fleets_empty_slice_returns_all() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("with-fleet", 200, "some-fleet"));
        t.upsert(sample_peer("standalone", 200));

        let keys = t.live_pubkeys_for_fleets(200, &[]);
        assert_eq!(keys.len(), 2);
    }

    /// Fleet filter still respects staleness — an old peer in the
    /// right fleet is still excluded.
    #[test]
    fn live_pubkeys_for_fleets_excludes_stale_match() {
        let fleet = "f-pk";
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("fresh", 200, fleet));
        t.upsert(sample_peer_in_fleet("stale", 10, fleet));

        let keys = t.live_pubkeys_for_fleets(200, &[fleet.to_string()]);
        assert_eq!(keys.len(), 1);
        assert!(keys[0].contains("fresh"));
    }

    /// A standalone peer (empty `fleet_pubkey`) is excluded under fleet
    /// mode — they aren't a member of the local user's fleet.
    #[test]
    fn live_pubkeys_for_fleets_excludes_standalone_peer_under_fleet_filter() {
        let fleet = "f-pk";
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("member", 200, fleet));
        t.upsert(sample_peer("rogue-standalone", 200)); // empty fleet_pubkey

        let keys = t.live_pubkeys_for_fleets(200, &[fleet.to_string()]);
        assert_eq!(keys.len(), 1);
        assert!(keys[0].contains("member"));
    }

    /// ADR-018 rekey window: the founder accepts both the new fleet
    /// pubkey (its own steady-state identity) and every unexpired
    /// retired fleet pubkey. Surviving envoys still announcing under
    /// the old fleet during the 24h graceful window stay in the
    /// recipient set. Without this, mid-window writes on the founder
    /// encrypt to a smaller recipient set than steady state — the
    /// "file appeared on desk + homeserver but not laptop" failure
    /// mode pending #2 was opened against.
    #[test]
    fn live_pubkeys_for_fleets_widens_during_rekey_window() {
        let new_fleet = "fleet-pk-new";
        let old_fleet = "fleet-pk-old";
        let mut t = PeerTable::empty("dan");
        // Founder has rekeyed and re-pinned to new_fleet. A laptop
        // that has also rekeyed announces under new_fleet.
        t.upsert(sample_peer_in_fleet("rekeyed-laptop", 200, new_fleet));
        // A desktop that hasn't rekeyed yet still announces under
        // the old fleet pubkey.
        t.upsert(sample_peer_in_fleet("not-yet-rekeyed-desktop", 200, old_fleet));
        // Some unrelated peer in a different fleet must stay out.
        t.upsert(sample_peer_in_fleet("alice-host", 200, "fleet-pk-alice"));

        let accepted = vec![new_fleet.to_string(), old_fleet.to_string()];
        let keys = t.live_pubkeys_for_fleets(200, &accepted);
        assert_eq!(keys.len(), 2, "both rekeyed and not-yet-rekeyed peers count");
        assert!(keys.iter().any(|k| k.contains("rekeyed-laptop")));
        assert!(keys.iter().any(|k| k.contains("not-yet-rekeyed-desktop")));
        assert!(
            !keys.iter().any(|k| k.contains("alice-host")),
            "unrelated fleets stay filtered out even with multiple accepted pubkeys"
        );
    }

    // ---- ADR-016 follow-up: TOFU pinning ----

    /// First-sighting fleet pubkey gets pinned for future enforcement.
    #[test]
    fn upsert_pins_fleet_pubkey_on_first_sighting() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("alpha", 100, "fleet-pk-1"));
        let stored = t.peers.get("alpha").unwrap();
        assert_eq!(stored.pinned_fleet_pubkey, "fleet-pk-1");
    }

    /// First sighting with empty fleet_pubkey leaves the pin empty —
    /// the peer is running standalone and we don't want to pre-pin
    /// anything. They can later announce a fleet pubkey and have it
    /// TOFU-pinned at that point.
    #[test]
    fn upsert_first_sighting_standalone_keeps_pin_empty() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("alpha", 100));
        let stored = t.peers.get("alpha").unwrap();
        assert_eq!(stored.pinned_fleet_pubkey, "");
    }

    /// A peer that started standalone and later adopts self-fleet has
    /// its pin set on the next non-stale heartbeat. Mirrors the
    /// real-world flow: user runs `self-fleet init` after cryptsync
    /// was already running.
    #[test]
    fn upsert_pins_when_peer_adopts_fleet_mid_session() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("alpha", 100));
        // Same hostname/pubkey, later heartbeat, now carrying a
        // fleet_pubkey for the first time.
        let mut later = sample_peer_in_fleet("alpha", 150, "fleet-pk-X");
        // Match existing pubkey/sign_pubkey so the rotation branch
        // doesn't fire — this is a heartbeat-shaped update.
        later.pubkey = t.peers.get("alpha").unwrap().pubkey.clone();
        t.upsert(later);
        let stored = t.peers.get("alpha").unwrap();
        assert_eq!(stored.pinned_fleet_pubkey, "fleet-pk-X");
        assert_eq!(stored.fleet_pubkey, "fleet-pk-X");
    }

    /// Once pinned, an announce under a different fleet_pubkey is
    /// rejected — upsert returns false (no mutation), the existing
    /// entry stays unchanged.
    #[test]
    fn upsert_rejects_mismatched_fleet_pubkey_after_pinning() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("alpha", 100, "fleet-pk-1"));
        // Attacker (or rotated peer) announces a different fleet pubkey.
        let spoofed = sample_peer_in_fleet("alpha", 200, "fleet-pk-2");
        let changed = t.upsert(spoofed);
        assert!(!changed, "upsert must reject TOFU mismatch");
        let stored = t.peers.get("alpha").unwrap();
        assert_eq!(stored.fleet_pubkey, "fleet-pk-1", "original fleet_pubkey unchanged");
        assert_eq!(stored.pinned_fleet_pubkey, "fleet-pk-1", "pin stays pinned");
        assert_eq!(stored.last_seen, 100, "last_seen must not advance on rejection");
    }

    /// A pinned peer announcing standalone (empty fleet_pubkey) is
    /// rejected — same protection as a different-fleet announce. If
    /// the operator legitimately wants to demote the peer to
    /// standalone they must clear with `forget`.
    #[test]
    fn upsert_rejects_pinned_peer_reverting_to_standalone() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("alpha", 100, "fleet-pk-1"));
        let reverted = sample_peer("alpha", 200); // empty fleet_pubkey
        let changed = t.upsert(reverted);
        assert!(!changed);
        assert_eq!(t.peers.get("alpha").unwrap().pinned_fleet_pubkey, "fleet-pk-1");
    }

    /// Matching fleet pubkey on subsequent heartbeats is fine and
    /// advances last_seen as usual.
    #[test]
    fn upsert_accepts_matching_fleet_pubkey_heartbeat() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("alpha", 100, "fleet-pk-1"));
        let mut hb = sample_peer_in_fleet("alpha", 200, "fleet-pk-1");
        hb.pubkey = t.peers.get("alpha").unwrap().pubkey.clone();
        // Heartbeat returns false (not a recipient-set change) but
        // last_seen advances and the pin stays put.
        assert!(!t.upsert(hb));
        assert_eq!(t.peers.get("alpha").unwrap().last_seen, 200);
        assert_eq!(t.peers.get("alpha").unwrap().pinned_fleet_pubkey, "fleet-pk-1");
    }

    /// Operator recovery: clearing the peer with `forget` lets the
    /// next announce TOFU-pin a fresh fleet_pubkey. Documented admin
    /// flow when an envoy legitimately rotates self-fleet.
    #[test]
    fn upsert_after_forget_repins_with_new_fleet_pubkey() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_fleet("alpha", 100, "fleet-pk-1"));
        assert!(t.forget("alpha"));
        // Fresh announce after operator-cleared state — TOFU re-pins.
        let changed = t.upsert(sample_peer_in_fleet("alpha", 200, "fleet-pk-2"));
        assert!(changed);
        assert_eq!(t.peers.get("alpha").unwrap().pinned_fleet_pubkey, "fleet-pk-2");
    }
}
