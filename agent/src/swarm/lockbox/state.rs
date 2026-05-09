// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Lockbox on-wire + on-disk state.
//!
//! - **Announcement** — the UDP multicast / cross-subnet unicast
//!   payload. Carries one envoy's pubkey for one user. Unlike
//!   longdrawer's announcement (which piggybacks full file + tombstone
//!   state), lockbox's is deliberately tiny: push/pull are
//!   event-driven, not inferred from announcements.
//!
//! - **PeerEntry** — one known peer's view of itself: hostname,
//!   pubkey, last-seen timestamp. Stored in `peers.json` keyed by
//!   hostname.
//!
//! - **PeerTable** — the whole per-user peer directory, persisted to
//!   `/var/lib/vigo/swarm/lockbox/<user>/peers.json` (root-owned, 0600).

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

/// One lockbox peer as observed by this envoy.
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
    /// the UDP multicast packet source or the `/lockbox/announce`
    /// unicast sender's TCP peer address. Required for the pusher —
    /// `peers.json` records hostnames, but the push HTTP client
    /// needs an IP to connect to. `None` if the peer is known only
    /// through recipients.txt (e.g. pre-populated manually).
    #[serde(default)]
    pub last_seen_addr: Option<String>,
    /// Hex-encoded puddle pubkey the peer announced (ADR-016).
    /// Empty when the peer is running standalone (puddle not
    /// initialized) or the announcement predates 0.35.x. Recipient
    /// resolution under puddle mode filters peers whose `puddle_pubkey`
    /// doesn't match the local user's puddle — the gossip layer
    /// is puddle-aware while age keys stay per-envoy.
    #[serde(default)]
    pub puddle_pubkey: String,
    /// Trust-on-first-use pinned puddle pubkey (ADR-016 follow-up).
    /// Set on first sighting from `puddle_pubkey`; subsequent
    /// announces under a different puddle_pubkey are rejected at
    /// upsert time. Empty when the peer was first seen running
    /// standalone — once a puddle pubkey is pinned, we never let the
    /// peer "revert" without an admin clearing the entry. Mirrors
    /// `swarmgit::peer_pubkey_cache`'s TOFU shape, embedded
    /// per-peer here because lockbox's PeerTable is already
    /// per-user and that's the right scope for lockbox TOFU.
    #[serde(default)]
    pub pinned_puddle_pubkey: String,
}

/// Outcome of a `PeerTable::upsert` call. Distinguishes the three
/// persistence-relevant cases so callers can drive both the
/// "should-save" decision and the "real-change for recipients" decision
/// from a single match. Heartbeats advance `last_seen` in memory and
/// MUST be persisted so `prune_stale` doesn't churn entries every
/// 90s — see `needs_save`. Only `RealChange` regenerates
/// `recipients.txt` (recipient set actually shifted).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum UpsertOutcome {
    /// Identical announce or older-heartbeat / TOFU rejection — no
    /// in-memory mutation worth persisting.
    Unchanged,
    /// Same pubkey + sign_pubkey, advancing `last_seen` (or first-set
    /// of `puddle_pubkey` between heartbeats). Disk save required so
    /// the prune cycle sees current liveness; recipients unchanged.
    Heartbeat,
    /// New peer (first sighting) or pubkey/sign_pubkey rotation —
    /// recipient set may have shifted. Disk save + recipients regen.
    RealChange,
}

impl UpsertOutcome {
    /// True when the in-memory mutation needs to land on disk. Both
    /// `Heartbeat` and `RealChange` mutate; only `Unchanged` skips.
    pub fn needs_save(&self) -> bool {
        matches!(self, UpsertOutcome::Heartbeat | UpsertOutcome::RealChange)
    }

    /// True only for changes that affect the recipient set. Callers
    /// gate `regenerate_recipients` on this — heartbeats keep the
    /// existing recipients.txt valid.
    pub fn is_real_change(&self) -> bool {
        matches!(self, UpsertOutcome::RealChange)
    }

    /// Save-log reason corresponding to this outcome. `None` when
    /// no save is needed.
    pub fn save_reason(&self) -> Option<&'static str> {
        match self {
            UpsertOutcome::RealChange => Some("real-change"),
            UpsertOutcome::Heartbeat => Some("heartbeat"),
            UpsertOutcome::Unchanged => None,
        }
    }
}

/// Per-user peer table. Persisted at
/// `/var/lib/vigo/swarm/lockbox/<user>/peers.json`.
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
                    "lockbox: malformed peers.json, starting fresh"
                );
                Self::empty(user)
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// Atomic save via temp + rename. Root-owned, 0600.
    ///
    /// `reason` names the path that triggered the save — one of
    /// `first-sighting | rotation | heartbeat | prune | leave`. INFO
    /// logged so journal readers can tell heartbeat churn from genuine
    /// recipient-set changes without re-reading the diff. Added in the
    /// 0.39.x heartbeat-persistence fix; before that there was zero
    /// log on save, which is what hid the prune→first-sight cycle bug.
    pub fn save(&self, reason: &'static str) -> Result<()> {
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

        // User-side mirror — best-effort. Lets user-context CLI
        // verbs read the table without sudo (e.g. `lockbox status`'s
        // recipients-vs-members gap line). Same pattern as
        // tombstones / leaves / resurrects.
        super::write_user_mirror_json(&self.user, "peers.json", &json);

        tracing::info!(
            user = %self.user,
            peer_count = self.peers.len(),
            reason = reason,
            "lockbox: peer table saved"
        );
        Ok(())
    }

    /// CLI-facing load that reads the user-side mirror at
    /// `~/.lockbox/peers.json` instead of the daemon-only
    /// canonical path. Used by user-context verbs that need to
    /// inspect peer-table state — `lockbox status`'s
    /// recipients-vs-members comparison is the first consumer.
    /// Same fall-through-to-empty semantics as `load`.
    pub fn load_user(user: &str, home: &std::path::Path) -> Self {
        let path = config::peers_user_path(home);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_else(|e| {
                tracing::warn!(
                    path = %path.display(),
                    error = %e,
                    "lockbox: malformed user-mirror peers.json, starting fresh"
                );
                Self::empty(user)
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// Atomic load → mutate → save under the per-user peers lock.
    /// Use this for any read-modify-write (upsert from announce,
    /// forget, prune_stale) — it prevents the lost-update race
    /// where two concurrent callers both load the same starting
    /// state and the second save() overwrites the first.
    ///
    /// Closure returns `Some(reason)` to save with that reason
    /// stamped into the save log line, or `None` to skip the save.
    /// For announce upserts, `UpsertOutcome::save_reason()` produces
    /// the right value directly.
    pub fn modify<F>(user: &str, f: F) -> Result<()>
    where
        F: FnOnce(&mut Self) -> Option<&'static str>,
    {
        let lock = super::locks::user_lock("peers", user);
        let _guard = lock.lock().unwrap_or_else(|p| p.into_inner());
        let mut table = Self::load(user);
        if let Some(reason) = f(&mut table) {
            table.save(reason)?;
        }
        Ok(())
    }

    /// Insert-or-update. Returns one of:
    ///
    /// - `UpsertOutcome::RealChange` — first sighting, or
    ///   pubkey/sign_pubkey rotation. Caller persists + regenerates
    ///   `recipients.txt`.
    /// - `UpsertOutcome::Heartbeat` — same pubkey + sign_pubkey but
    ///   advancing `last_seen`, or first-set of `puddle_pubkey`
    ///   between heartbeats. Caller persists (so prune cycle sees
    ///   current liveness) but does NOT regenerate recipients.
    /// - `UpsertOutcome::Unchanged` — identical announce, older-
    ///   heartbeat reorder, or TOFU rejection. Caller skips save.
    ///
    /// ADR-016 follow-up: TOFU rejects announcements whose
    /// `puddle_pubkey` disagrees with the previously-pinned value for
    /// the same hostname — returns `Unchanged` with no mutation. The
    /// receive-side caller detects TOFU separately (via the pre-upsert
    /// pin snapshot) and logs the rejection. Operator recovery is to
    /// clear the peer entry — `vigo swarm lockbox forget <host>`
    /// does the trick. First sighting under a non-empty
    /// `puddle_pubkey` pins it to `pinned_puddle_pubkey` for future
    /// TOFU checks.
    pub fn upsert(&mut self, mut entry: PeerEntry) -> UpsertOutcome {
        match self.peers.get(&entry.hostname) {
            Some(existing) => {
                // TOFU check — once we've pinned a non-empty puddle
                // pubkey, the peer must keep announcing the same one.
                // An empty incoming under a non-empty pin is rejected
                // (peer "reverted to standalone" looks identical on
                // the wire to a same-LAN spoofer, so be strict).
                if !existing.pinned_puddle_pubkey.is_empty()
                    && existing.pinned_puddle_pubkey != entry.puddle_pubkey
                {
                    // Mismatch — drop the announce. Receive-side
                    // caller logs the warn message with full detail.
                    return UpsertOutcome::Unchanged;
                }
                if existing.pubkey != entry.pubkey || existing.sign_pubkey != entry.sign_pubkey {
                    // Pubkey rotation (age or sign) — real change.
                    // Carry the pin forward: it's already empty (and
                    // entry.puddle_pubkey may legitimately set it for
                    // the first time) or it's non-empty and matches
                    // entry.puddle_pubkey (verified above).
                    entry.pinned_puddle_pubkey = if entry.puddle_pubkey.is_empty() {
                        existing.pinned_puddle_pubkey.clone()
                    } else {
                        entry.puddle_pubkey.clone()
                    };
                    self.peers.insert(entry.hostname.clone(), entry);
                    UpsertOutcome::RealChange
                } else if entry.last_seen > existing.last_seen {
                    // Heartbeat update — persisted so prune cycle
                    // doesn't drop fresh peers every 90s. Not a
                    // recipient-set change. Refresh addr so the
                    // pusher has current routing info. Preserve the
                    // existing pin; refresh puddle_pubkey only if the
                    // pin was already empty (legitimate first-set).
                    let mut updated = existing.clone();
                    updated.last_seen = entry.last_seen;
                    if entry.last_seen_addr.is_some() {
                        updated.last_seen_addr = entry.last_seen_addr;
                    }
                    if updated.pinned_puddle_pubkey.is_empty()
                        && !entry.puddle_pubkey.is_empty()
                    {
                        // First-sighting puddle pubkey pin (peer just
                        // adopted puddle between heartbeats).
                        updated.puddle_pubkey = entry.puddle_pubkey;
                        updated.pinned_puddle_pubkey = updated.puddle_pubkey.clone();
                    }
                    self.peers.insert(entry.hostname.clone(), updated);
                    UpsertOutcome::Heartbeat
                } else {
                    UpsertOutcome::Unchanged
                }
            }
            None => {
                // First sighting of this hostname. TOFU-pin whatever
                // puddle_pubkey the announce carried (empty stays
                // empty — peer is running standalone).
                if entry.pinned_puddle_pubkey.is_empty() {
                    entry.pinned_puddle_pubkey = entry.puddle_pubkey.clone();
                }
                self.peers.insert(entry.hostname.clone(), entry);
                UpsertOutcome::RealChange
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

    /// Clear `pinned_puddle_pubkey` on every peer whose recorded
    /// `puddle_pubkey` matches `retired`. Returns the number of
    /// entries adjusted. Used by puddle rekey (ADR-018) so peers
    /// announcing under the post-rotation puddle pubkey don't trip
    /// the TOFU mismatch check at upsert time. The `puddle_pubkey`
    /// field itself is left alone — `live_pubkeys_for_puddles` will
    /// filter the entry out until the peer's next announce updates
    /// it, which is the correct behavior during a rotation rollout.
    pub fn clear_pins_for_puddle(&mut self, retired: &str) -> usize {
        let mut adjusted = 0usize;
        for entry in self.peers.values_mut() {
            if entry.puddle_pubkey == retired
                && !entry.pinned_puddle_pubkey.is_empty()
            {
                entry.pinned_puddle_pubkey.clear();
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

    /// Puddle-aware variant of [`live_pubkeys`] (ADR-016). The caller
    /// passes the set of puddle pubkeys whose peers count as recipient
    /// candidates. An empty slice means "no puddle filter" — standalone
    /// mode, identical to [`live_pubkeys`].
    ///
    /// Steady state passes a one-element slice (the local user's
    /// puddle pubkey). During an ADR-018 rekey rollout, the founder
    /// passes the new puddle pubkey *and* every unexpired retired puddle
    /// pubkey from `~/.vigo-puddle/retired_puddles.json`, so peers still
    /// announcing under the old puddle during the 24h graceful window
    /// stay in the recipient set rather than silently disappearing
    /// until they finish their own rekey.
    ///
    /// Per-envoy age keys stay deliberately preserved: even when
    /// filtering by puddle membership, the per-peer pubkeys this
    /// returns are each peer's individual age x25519 pubkey, not a
    /// shared puddle decryption key. ADR-016 calls this out as
    /// load-bearing — see `mod.rs::regenerate_recipients` for the
    /// invariant comment.
    pub fn live_pubkeys_for_puddles(
        &self,
        now: i64,
        accepted_puddle_pubkeys: &[String],
    ) -> Vec<String> {
        let mut keys: Vec<String> = self
            .peers
            .values()
            .filter(|p| now - p.last_seen <= STALE_SECS)
            .filter(|p| {
                if accepted_puddle_pubkeys.is_empty() {
                    return true;
                }
                accepted_puddle_pubkeys
                    .iter()
                    .any(|fp| p.puddle_pubkey == *fp)
            })
            .map(|p| p.pubkey.clone())
            .collect();
        keys.sort();
        keys.dedup();
        keys
    }
}

/// UDP announcement payload. Line-delimited wire format:
/// `LOCKBOX-1\n<json>`.
///
/// `generation` is a monotonic counter per (envoy, user) incremented
/// on each boot / identity rotation. Reserved for future key-rotation
/// validation; current code doesn't use it beyond longdrawering it back.
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
/// a `GET /lockbox/tombstones/<user>` to this sender when they
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
    /// Hex-encoded 16-byte digest of the per-user `ResurrectsTable`
    /// (`(filename, resurrect_time)` pairs). Mirrors
    /// `tombstones_digest` shape — receivers compare against their
    /// local digest and pull the announcer's full resurrects table on
    /// mismatch via `GET /swarm/lockbox/resurrects/<user>`. Empty
    /// string from pre-0.39.x peers; receivers treat empty as "skip
    /// resurrects catch-up for this peer."
    #[serde(default)]
    pub resurrects_digest: String,
    /// Hex-encoded puddle Ed25519 pubkey for the announcing user
    /// (ADR-016). Empty string when the user is running standalone
    /// or the announce predates 0.35.x. Receivers under puddle mode
    /// filter peers whose pubkey here doesn't match the local
    /// user's puddle — see `state::PeerTable::live_pubkeys_for_puddles`.
    #[serde(default)]
    pub puddle_pubkey: String,
    /// Optional puddle certificate (ADR-016 follow-up). Closes the
    /// same-LAN first-contact race that TOFU pinning has on its own:
    /// receivers verify the cert's signature against `puddle_pubkey`
    /// before accepting the puddle claim. `None` when the announcer
    /// hasn't issued a cert yet (slice 1 / slice 2 peers, or
    /// `lockbox puddle renew-cert` not run); the announce still
    /// passes through to receivers under TOFU rules but receivers
    /// will warn-log the absence so operators can tell.
    #[serde(default)]
    pub puddle_cert: Option<super::puddle_cert::PuddleCert>,
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
            puddle_pubkey: String::new(),
            pinned_puddle_pubkey: String::new(),
        }
    }

    fn sample_peer_in_puddle(name: &str, ts: i64, puddle: &str) -> PeerEntry {
        PeerEntry {
            hostname: name.to_string(),
            pubkey: format!("age1{:0>52}", name),
            sign_pubkey: String::new(),
            last_seen: ts,
            last_seen_addr: None,
            puddle_pubkey: puddle.to_string(),
            // Pin starts empty — upsert pins on first sighting.
            pinned_puddle_pubkey: String::new(),
        }
    }

    #[test]
    fn upsert_new_peer_is_a_real_change() {
        let mut t = PeerTable::empty("dan");
        let outcome = t.upsert(sample_peer("host-a", 100));
        assert_eq!(outcome, UpsertOutcome::RealChange);
        assert!(outcome.is_real_change());
        assert!(outcome.needs_save());
    }

    #[test]
    fn upsert_heartbeat_persists_but_isnt_a_real_change() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("host-a", 100));
        // Same pubkey, newer last_seen — heartbeat. Must persist (so
        // prune doesn't churn), but recipients don't change.
        let outcome = t.upsert(sample_peer("host-a", 130));
        assert_eq!(outcome, UpsertOutcome::Heartbeat);
        assert!(!outcome.is_real_change());
        assert!(outcome.needs_save());
        assert_eq!(outcome.save_reason(), Some("heartbeat"));
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
            puddle_pubkey: String::new(),
            pinned_puddle_pubkey: String::new(),
        };
        assert_eq!(t.upsert(rotated), UpsertOutcome::RealChange);
    }

    #[test]
    fn upsert_older_heartbeat_is_unchanged() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("host-a", 200));
        // Older announcement — reordering on the wire; don't regress.
        let outcome = t.upsert(sample_peer("host-a", 100));
        assert_eq!(outcome, UpsertOutcome::Unchanged);
        assert!(!outcome.needs_save());
        assert_eq!(t.peers.get("host-a").unwrap().last_seen, 200);
    }

    #[test]
    fn upsert_tofu_rejection_is_unchanged() {
        // Existing entry pinned to fleet-a; incoming announces fleet-b
        // → TOFU mismatch → rejected with no mutation.
        let mut t = PeerTable::empty("dan");
        let mut e = sample_peer_in_puddle("host-a", 100, "fleet-a");
        // Force the pin (upsert would do this on first sighting; do
        // it explicitly so the test reads top-down).
        e.pinned_puddle_pubkey = "fleet-a".to_string();
        t.peers.insert("host-a".into(), e);
        let incoming = sample_peer_in_puddle("host-a", 200, "fleet-b");
        let outcome = t.upsert(incoming);
        assert_eq!(outcome, UpsertOutcome::Unchanged);
        // Existing entry untouched.
        assert_eq!(t.peers.get("host-a").unwrap().puddle_pubkey, "fleet-a");
        assert_eq!(t.peers.get("host-a").unwrap().last_seen, 100);
    }

    #[test]
    fn clear_pins_for_puddle_only_touches_matching_peers() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_puddle("rotating", 100, "fleet-a"));
        t.upsert(sample_peer_in_puddle("other", 100, "fleet-b"));
        t.upsert(sample_peer("standalone", 100));
        // Sanity: TOFU pinned each fleet peer on first sighting.
        assert_eq!(
            t.peers.get("rotating").unwrap().pinned_puddle_pubkey,
            "fleet-a"
        );
        assert_eq!(t.peers.get("other").unwrap().pinned_puddle_pubkey, "fleet-b");
        assert_eq!(t.peers.get("standalone").unwrap().pinned_puddle_pubkey, "");

        let cleared = t.clear_pins_for_puddle("fleet-a");
        assert_eq!(cleared, 1);
        // Only "rotating" lost its pin; the others are untouched.
        assert_eq!(t.peers.get("rotating").unwrap().pinned_puddle_pubkey, "");
        assert_eq!(t.peers.get("other").unwrap().pinned_puddle_pubkey, "fleet-b");
        assert_eq!(t.peers.get("standalone").unwrap().pinned_puddle_pubkey, "");
        // The puddle_pubkey field itself is left alone — recipient
        // resolution will re-pin via the next announce.
        assert_eq!(t.peers.get("rotating").unwrap().puddle_pubkey, "fleet-a");
    }

    #[test]
    fn clear_pins_for_puddle_returns_zero_when_no_match() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_puddle("a", 100, "fleet-a"));
        let cleared = t.clear_pins_for_puddle("fleet-z");
        assert_eq!(cleared, 0);
        assert_eq!(t.peers.get("a").unwrap().pinned_puddle_pubkey, "fleet-a");
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
            puddle_pubkey: String::new(),
            pinned_puddle_pubkey: String::new(),
        });
        t.upsert(PeerEntry {
            hostname: "a".into(),
            pubkey: "age1a".into(),
            sign_pubkey: String::new(),
            last_seen: 200,
            last_seen_addr: None,
            puddle_pubkey: String::new(),
            pinned_puddle_pubkey: String::new(),
        });
        t.upsert(PeerEntry {
            hostname: "stale".into(),
            pubkey: "age1stale".into(),
            sign_pubkey: String::new(),
            last_seen: 10,
            last_seen_addr: None,
            puddle_pubkey: String::new(),
            pinned_puddle_pubkey: String::new(),
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
            resurrects_digest: String::new(),
            puddle_pubkey: String::new(),
            puddle_cert: None,
        };
        let bytes = serde_json::to_vec(&a).unwrap();
        let parsed: Announcement = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(a, parsed);
    }

    /// Pre-0.35.x announcements arrive without `puddle_pubkey`. Receivers
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
        assert_eq!(parsed.puddle_pubkey, "");
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
        t.upsert(sample_peer_in_puddle("alpha", 200, fleet));
        t.upsert(sample_peer_in_puddle("beta", 200, fleet));

        let keys = t.live_pubkeys_for_puddles(200, &[fleet.to_string()]);

        // Two envoys → two distinct age pubkeys, NOT one fleet key.
        assert_eq!(keys.len(), 2, "expected one age pubkey per envoy");
        assert!(
            keys.iter().all(|k| k.starts_with("age1")),
            "every recipient must be a per-envoy age pubkey, not the puddle pubkey"
        );
        assert!(keys.iter().all(|k| !k.contains(fleet)),
            "the fleet ed25519 pubkey must never appear as an age recipient");
        assert_ne!(keys[0], keys[1], "the two envoys must contribute distinct keys");
    }

    /// Cross-fleet contamination: a peer announcing under a different
    /// puddle pubkey is filtered out. Without this, every envoy on the
    /// LAN would land in everyone else's recipients regardless of
    /// trust scope.
    #[test]
    fn live_pubkeys_for_puddles_filters_other_fleets() {
        let fleet_dan = "fleet-pk-dan";
        let fleet_alice = "fleet-pk-alice";
        let mut t = PeerTable::empty("multi");
        t.upsert(sample_peer_in_puddle("dan-laptop", 200, fleet_dan));
        t.upsert(sample_peer_in_puddle("dan-desktop", 200, fleet_dan));
        t.upsert(sample_peer_in_puddle("alice-laptop", 200, fleet_alice));

        let dan_recipients = t.live_pubkeys_for_puddles(200, &[fleet_dan.to_string()]);
        assert_eq!(dan_recipients.len(), 2);
        assert!(dan_recipients.iter().any(|k| k.contains("dan-laptop")));
        assert!(dan_recipients.iter().any(|k| k.contains("dan-desktop")));
        assert!(
            !dan_recipients.iter().any(|k| k.contains("alice")),
            "alice's envoys must not be recipients of dan's content"
        );
    }

    /// Standalone mode (empty slice) returns every live peer regardless
    /// of `puddle_pubkey` — preserves the legacy gossip-only behavior
    /// for users who haven't opted into puddle.
    #[test]
    fn live_pubkeys_for_puddles_empty_slice_returns_all() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_puddle("with-fleet", 200, "some-fleet"));
        t.upsert(sample_peer("standalone", 200));

        let keys = t.live_pubkeys_for_puddles(200, &[]);
        assert_eq!(keys.len(), 2);
    }

    /// Fleet filter still respects staleness — an old peer in the
    /// right fleet is still excluded.
    #[test]
    fn live_pubkeys_for_puddles_excludes_stale_match() {
        let fleet = "f-pk";
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_puddle("fresh", 200, fleet));
        t.upsert(sample_peer_in_puddle("stale", 10, fleet));

        let keys = t.live_pubkeys_for_puddles(200, &[fleet.to_string()]);
        assert_eq!(keys.len(), 1);
        assert!(keys[0].contains("fresh"));
    }

    /// A peer in a different puddle is excluded — they aren't a
    /// member of the local user's puddle. Post-ADR-020 announces with
    /// empty `puddle_pubkey` are dropped at the front door before
    /// reaching the peer table at all; this test guards the residual
    /// "wrong puddle pubkey" case (which is still possible from an
    /// announce-receive that bypasses the front-door drop).
    #[test]
    fn live_pubkeys_for_puddles_excludes_peer_under_different_puddle() {
        let fleet = "f-pk";
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_puddle("member", 200, fleet));
        t.upsert(sample_peer_in_puddle("other-puddle", 200, "other-pk"));

        let keys = t.live_pubkeys_for_puddles(200, &[fleet.to_string()]);
        assert_eq!(keys.len(), 1);
        assert!(keys[0].contains("member"));
    }

    /// ADR-018 rekey window: the founder accepts both the new fleet
    /// pubkey (its own steady-state identity) and every unexpired
    /// retired puddle pubkey. Surviving envoys still announcing under
    /// the old puddle during the 24h graceful window stay in the
    /// recipient set. Without this, mid-window writes on the founder
    /// encrypt to a smaller recipient set than steady state — the
    /// "file appeared on desk + homeserver but not laptop" failure
    /// mode pending #2 was opened against.
    #[test]
    fn live_pubkeys_for_puddles_widens_during_rekey_window() {
        let new_fleet = "fleet-pk-new";
        let old_fleet = "fleet-pk-old";
        let mut t = PeerTable::empty("dan");
        // Founder has rekeyed and re-pinned to new_fleet. A laptop
        // that has also rekeyed announces under new_fleet.
        t.upsert(sample_peer_in_puddle("rekeyed-laptop", 200, new_fleet));
        // A desktop that hasn't rekeyed yet still announces under
        // the old puddle pubkey.
        t.upsert(sample_peer_in_puddle("not-yet-rekeyed-desktop", 200, old_fleet));
        // Some unrelated peer in a different fleet must stay out.
        t.upsert(sample_peer_in_puddle("alice-host", 200, "fleet-pk-alice"));

        let accepted = vec![new_fleet.to_string(), old_fleet.to_string()];
        let keys = t.live_pubkeys_for_puddles(200, &accepted);
        assert_eq!(keys.len(), 2, "both rekeyed and not-yet-rekeyed peers count");
        assert!(keys.iter().any(|k| k.contains("rekeyed-laptop")));
        assert!(keys.iter().any(|k| k.contains("not-yet-rekeyed-desktop")));
        assert!(
            !keys.iter().any(|k| k.contains("alice-host")),
            "unrelated fleets stay filtered out even with multiple accepted pubkeys"
        );
    }

    // ---- ADR-016 follow-up: TOFU pinning ----

    /// First-sighting puddle pubkey gets pinned for future enforcement.
    #[test]
    fn upsert_pins_puddle_pubkey_on_first_sighting() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_puddle("alpha", 100, "fleet-pk-1"));
        let stored = t.peers.get("alpha").unwrap();
        assert_eq!(stored.pinned_puddle_pubkey, "fleet-pk-1");
    }

    /// First sighting with empty puddle_pubkey leaves the pin empty.
    /// Post-ADR-020 the announce-receive path drops empty-puddle
    /// announces at the front door before reaching upsert, but the
    /// upsert-level invariant (no auto-pin without explicit
    /// puddle_pubkey on the entry) is preserved as a defensive
    /// guard for direct test-driven inserts and any future code path
    /// that reaches upsert without going through receive.
    #[test]
    fn upsert_first_sighting_no_puddle_keeps_pin_empty() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("alpha", 100));
        let stored = t.peers.get("alpha").unwrap();
        assert_eq!(stored.pinned_puddle_pubkey, "");
    }

    /// A peer that adopts a puddle pubkey on a later heartbeat has
    /// its pin set at that point. Mirrors the real-world flow: user
    /// runs `puddle init` after lockbox was already running.
    #[test]
    fn upsert_pins_when_peer_adopts_fleet_mid_session() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer("alpha", 100));
        // Same hostname/pubkey, later heartbeat, now carrying a
        // puddle_pubkey for the first time.
        let mut later = sample_peer_in_puddle("alpha", 150, "fleet-pk-X");
        // Match existing pubkey/sign_pubkey so the rotation branch
        // doesn't fire — this is a heartbeat-shaped update.
        later.pubkey = t.peers.get("alpha").unwrap().pubkey.clone();
        t.upsert(later);
        let stored = t.peers.get("alpha").unwrap();
        assert_eq!(stored.pinned_puddle_pubkey, "fleet-pk-X");
        assert_eq!(stored.puddle_pubkey, "fleet-pk-X");
    }

    /// Once pinned, an announce under a different puddle_pubkey is
    /// rejected — upsert returns false (no mutation), the existing
    /// entry stays unchanged.
    #[test]
    fn upsert_rejects_mismatched_puddle_pubkey_after_pinning() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_puddle("alpha", 100, "fleet-pk-1"));
        // Attacker (or rotated peer) announces a different puddle pubkey.
        let spoofed = sample_peer_in_puddle("alpha", 200, "fleet-pk-2");
        let outcome = t.upsert(spoofed);
        assert_eq!(outcome, UpsertOutcome::Unchanged, "upsert must reject TOFU mismatch");
        let stored = t.peers.get("alpha").unwrap();
        assert_eq!(stored.puddle_pubkey, "fleet-pk-1", "original puddle_pubkey unchanged");
        assert_eq!(stored.pinned_puddle_pubkey, "fleet-pk-1", "pin stays pinned");
        assert_eq!(stored.last_seen, 100, "last_seen must not advance on rejection");
    }

    /// A pinned peer announcing under a different puddle pubkey
    /// (including empty, in the unlikely case it bypasses the front
    /// door) is rejected — TOFU mismatch protection. Operator must
    /// clear with `forget` if the peer is legitimately moving.
    #[test]
    fn upsert_rejects_pinned_peer_under_different_puddle_pubkey() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_puddle("alpha", 100, "fleet-pk-1"));
        let reverted = sample_peer("alpha", 200); // empty puddle_pubkey
        let outcome = t.upsert(reverted);
        assert_eq!(outcome, UpsertOutcome::Unchanged);
        assert_eq!(t.peers.get("alpha").unwrap().pinned_puddle_pubkey, "fleet-pk-1");
    }

    /// Matching puddle pubkey on subsequent heartbeats is fine and
    /// advances last_seen as usual.
    #[test]
    fn upsert_accepts_matching_puddle_pubkey_heartbeat() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_puddle("alpha", 100, "fleet-pk-1"));
        let mut hb = sample_peer_in_puddle("alpha", 200, "fleet-pk-1");
        hb.pubkey = t.peers.get("alpha").unwrap().pubkey.clone();
        // Heartbeat persists last_seen but isn't a recipient-set
        // change. The pin stays put across the heartbeat.
        let outcome = t.upsert(hb);
        assert_eq!(outcome, UpsertOutcome::Heartbeat);
        assert!(outcome.needs_save());
        assert!(!outcome.is_real_change());
        assert_eq!(t.peers.get("alpha").unwrap().last_seen, 200);
        assert_eq!(t.peers.get("alpha").unwrap().pinned_puddle_pubkey, "fleet-pk-1");
    }

    /// Operator recovery: clearing the peer with `forget` lets the
    /// next announce TOFU-pin a fresh puddle_pubkey. Documented admin
    /// flow when an envoy legitimately rotates puddle.
    #[test]
    fn upsert_after_forget_repins_with_new_puddle_pubkey() {
        let mut t = PeerTable::empty("dan");
        t.upsert(sample_peer_in_puddle("alpha", 100, "fleet-pk-1"));
        assert!(t.forget("alpha"));
        // Fresh announce after operator-cleared state — TOFU re-pins.
        let outcome = t.upsert(sample_peer_in_puddle("alpha", 200, "fleet-pk-2"));
        assert_eq!(outcome, UpsertOutcome::RealChange);
        assert_eq!(t.peers.get("alpha").unwrap().pinned_puddle_pubkey, "fleet-pk-2");
    }

    /// User-mirror round-trip: a peer table serialized to the
    /// user-mirror path is read back identically by `load_user`.
    /// Exercises the format used by `~/.lockbox/peers.json` so
    /// the user-context status path can rely on the schema.
    #[test]
    fn load_user_round_trips_through_mirror_path() {
        let dir = tempfile::tempdir().unwrap();
        let home = dir.path();
        std::fs::create_dir_all(super::config::dot_dir(home)).unwrap();

        // Build a table with two peers — one in a puddle, one
        // standalone — so the round-trip covers both shapes.
        let mut original = PeerTable::empty("dan");
        original.upsert(sample_peer_in_puddle("alpha", 100, "fleet-pk-1"));
        original.upsert(sample_peer("standalone", 100));
        let json = serde_json::to_string_pretty(&original).unwrap();
        std::fs::write(super::config::peers_user_path(home), &json).unwrap();

        let loaded = PeerTable::load_user("dan", home);
        assert_eq!(loaded.user, original.user);
        assert_eq!(loaded.peers.len(), 2);
        assert_eq!(
            loaded.peers.get("alpha").unwrap().puddle_pubkey,
            "fleet-pk-1"
        );
        assert_eq!(loaded.peers.get("standalone").unwrap().puddle_pubkey, "");
    }

    /// Missing mirror file → empty table, never errors. Matches
    /// `TombstoneTable::load_user` semantics.
    #[test]
    fn load_user_returns_empty_when_mirror_absent() {
        let dir = tempfile::tempdir().unwrap();
        let loaded = PeerTable::load_user("dan", dir.path());
        assert_eq!(loaded.user, "dan");
        assert!(loaded.peers.is_empty());
    }
}
