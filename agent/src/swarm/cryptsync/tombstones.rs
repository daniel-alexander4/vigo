// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Cryptsync deletion state — the tombstone store.
//!
//! When a user removes a file from `~/cryptsync/`, the watcher fires
//! a delete event instead of a push event (see `watcher.rs`). The
//! pusher fans a signed DELETE envelope out to every live peer. On
//! the receiving side, the peer records a **tombstone** — a compact
//! `{filename, deletion_time, source_hostname}` record — in the
//! per-user tombstone store and removes its local copy of the file.
//!
//! Tombstones serve three purposes:
//!
//! 1. **Resurrection blocking.** A peer with an active tombstone for
//!    `foo.pdf` refuses PUSHes of `foo.pdf` whose age mtime is older
//!    than the tombstone's `deletion_time`, preventing a still-offline
//!    peer with a stale copy from silently repopulating a deleted
//!    file when it reconnects.
//! 2. **Idempotent delete.** Receiving the same DELETE twice is a
//!    no-op; the tombstone is just re-asserted with the same
//!    deletion_time.
//! 3. **Catch-up.** Announcements carry a 16-byte
//!    `tombstones_digest` (truncated SHA-256 over sorted
//!    `filename|deletion_time` pairs). Peers that observe a
//!    digest mismatch pull the full tombstone list from the
//!    announcer via `GET /cryptsync/tombstones/<user>` and merge.
//!    This means an envoy that was offline during a delete learns
//!    about the deletion on its next announcement cycle, not on
//!    some hopeful future pull attempt.
//!
//! Tombstones age out at `retention_days` (default 90). After GC,
//! the filename can be legitimately re-used for a new file — e.g.,
//! you deleted `notes.md` six months ago and today you encrypt a
//! fresh `notes.md` with entirely different contents.
//!
//! Persistence: `/var/lib/vigo/swarm/cryptsync/<username>/tombstones.json`
//! (root-owned, 0600) — canonical, daemon-only. After every save the
//! daemon also writes a user-readable mirror at
//! `~<user>/.cryptsync/tombstones.json` (user-owned, 0600) so `vigo swarm cryptsync
//! list` / `status` can read the table without sudo. The mirror is
//! best-effort: a failed mirror write never fails the canonical save.

use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::path::PathBuf;

use anyhow::{bail, Context, Result};
use ed25519_dalek::{Signer, Verifier};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use super::config;
use super::crypt;

/// Default retention window in seconds (90 days).
///
/// Covers realistic "laptop parked for an extended trip" scenarios
/// without letting tombstones pile up forever. Configurable per
/// user via `tombstone_retention_days` in `~/.cryptsync/config.yaml`.
pub const DEFAULT_RETENTION_SECS: i64 = 90 * 24 * 60 * 60;

/// Length of the on-announcement digest. 16 bytes (128 bits) gives
/// negligible collision probability across realistic tombstone
/// churn (millions of distinct `(filename, deletion_time)` pairs).
pub const DIGEST_LEN: usize = 16;

/// A single deletion record.
///
/// `deletion_time` is unix seconds at the moment of original
/// deletion on the source envoy. `source_hostname` is the envoy
/// that initiated the delete — retained for audit and for future
/// anti-forgery work, but tombstone validity is currently
/// enforced via the signed DELETE envelope, not this field.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct Tombstone {
    pub deletion_time: i64,
    pub source_hostname: String,
}

/// Per-user tombstone table. Persisted at
/// `/var/lib/vigo/swarm/cryptsync/<user>/tombstones.json`.
///
/// Keyed by filename for O(1) lookup during push/pull gating.
/// BTreeMap rather than HashMap so the serialized form is
/// deterministic — required for the on-announcement digest
/// to be stable across envoys that reconstructed the same
/// logical set in different insertion orders.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TombstoneTable {
    pub user: String,
    pub tombstones: BTreeMap<String, Tombstone>,
}

impl TombstoneTable {
    pub fn empty(user: &str) -> Self {
        Self {
            user: user.to_string(),
            tombstones: BTreeMap::new(),
        }
    }

    /// Load from disk, or return empty if the file is missing or
    /// malformed. Never errors — absence is the common case on
    /// first boot. Malformed content is logged, discarded, and the
    /// empty replacement is written back so subsequent loads stop
    /// re-parsing the same broken file (otherwise every reactor tick
    /// re-warns until the next legitimate save happens to land).
    pub fn load(user: &str) -> Self {
        let path = tombstones_path(user);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_else(|e| {
                tracing::warn!(
                    path = %path.display(),
                    error = %e,
                    "cryptsync: malformed tombstones.json, starting fresh and overwriting"
                );
                let empty = Self::empty(user);
                if let Err(save_err) = empty.save() {
                    tracing::warn!(
                        path = %path.display(),
                        error = %save_err,
                        "cryptsync: failed to overwrite malformed tombstones.json; will re-warn next load"
                    );
                }
                empty
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// CLI-facing load that reads the user-side mirror at
    /// `~/.cryptsync/tombstones.json` instead of the daemon-only canonical
    /// path. Used by `vigo swarm cryptsync list` / `status` (running as the
    /// unprivileged user). Same fall-through semantics as `load`.
    pub fn load_user(user: &str, home: &std::path::Path) -> Self {
        let path = config::tombstones_user_path(home);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_else(|e| {
                tracing::warn!(
                    path = %path.display(),
                    error = %e,
                    "cryptsync: malformed user-mirror tombstones.json, starting fresh"
                );
                Self::empty(user)
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// Atomic save via temp + rename. Canonical state is root-owned
    /// 0600 at `agent_state_dir/<user>/tombstones.json`. After the
    /// canonical write succeeds, a best-effort mirror is also written
    /// at `~<user>/.cryptsync/tombstones.json` (user-owned, 0600) so
    /// `vigo swarm cryptsync list` / `status` can read it without sudo.
    pub fn save(&self) -> Result<()> {
        let dir = config::agent_state_dir(&self.user);
        fs::create_dir_all(&dir).with_context(|| format!("creating {}", dir.display()))?;
        let path = tombstones_path(&self.user);
        let tmp = path.with_extension("json.tmp");
        let json = serde_json::to_string_pretty(self).context("serializing tombstone table")?;

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
        super::write_user_mirror_json(&self.user, "tombstones.json", &json);
        Ok(())
    }

    /// Atomic load → mutate → save under the per-user tombstone
    /// lock. Use this for any read-modify-write (upsert / merge / gc)
    /// — it prevents the lost-update race where two concurrent
    /// callers both load the same starting state and the second
    /// save() overwrites the first. The closure returns `true` if
    /// it mutated the table; the helper only saves when so.
    pub fn modify<F>(user: &str, f: F) -> Result<()>
    where
        F: FnOnce(&mut Self) -> bool,
    {
        let lock = super::locks::user_lock("tombstones", user);
        let _guard = lock.lock().unwrap_or_else(|p| p.into_inner());
        let mut table = Self::load(user);
        if f(&mut table) {
            table.save()?;
        }
        Ok(())
    }

    /// Insert-or-update a tombstone. Later `deletion_time` wins
    /// when two records race — e.g., you delete then re-delete
    /// a file that was re-published in between. Returns true if
    /// this call changed the stored tombstone (new entry OR
    /// deletion_time advanced), so callers can skip an unnecessary
    /// `save()` when the inbound record is a duplicate.
    pub fn upsert(&mut self, filename: &str, t: Tombstone) -> bool {
        match self.tombstones.get(filename) {
            Some(existing) if existing.deletion_time >= t.deletion_time => false,
            _ => {
                self.tombstones.insert(filename.to_string(), t);
                true
            }
        }
    }

    /// Remove a tombstone. Used when an operator re-publishes a
    /// file after the tombstone was recorded (new mtime wins) and
    /// during GC. Returns true if the entry was present.
    pub fn remove(&mut self, filename: &str) -> bool {
        self.tombstones.remove(filename).is_some()
    }

    /// Is `filename` currently tombstoned, and if so at what
    /// deletion_time? Returns `None` if no tombstone.
    pub fn active_deletion_time(&self, filename: &str) -> Option<i64> {
        self.tombstones.get(filename).map(|t| t.deletion_time)
    }

    /// Drop tombstones older than `retention_secs` relative to
    /// `now`. Returns the count dropped; caller should `save()`
    /// when > 0.
    pub fn gc(&mut self, now: i64, retention_secs: i64) -> usize {
        let cutoff = now - retention_secs;
        let before = self.tombstones.len();
        self.tombstones.retain(|_, t| t.deletion_time >= cutoff);
        before - self.tombstones.len()
    }

    /// 16-byte digest of the sorted `(filename, deletion_time)`
    /// pairs. Piggybacks on announcements so peers can detect
    /// divergence without carrying the full list every 30 s.
    /// BTreeMap iteration is sorted by key, so order is stable
    /// across envoys that hold the same logical set.
    pub fn digest(&self) -> [u8; DIGEST_LEN] {
        let mut hasher = Sha256::new();
        for (name, t) in &self.tombstones {
            hasher.update(name.as_bytes());
            hasher.update(b"|");
            hasher.update(t.deletion_time.to_be_bytes());
            hasher.update(b"\n");
        }
        let full = hasher.finalize();
        let mut out = [0u8; DIGEST_LEN];
        out.copy_from_slice(&full[..DIGEST_LEN]);
        out
    }

    /// Merge another table's entries into this one. Later
    /// `deletion_time` wins per filename. Returns true if any
    /// entry was added or advanced — caller should `save()` when
    /// so.
    pub fn merge(&mut self, other: &TombstoneTable) -> bool {
        let mut changed = false;
        for (name, t) in &other.tombstones {
            if self.upsert(name, t.clone()) {
                changed = true;
            }
        }
        changed
    }
}

/// `/var/lib/vigo/swarm/cryptsync/<user>/tombstones.json`.
pub fn tombstones_path(username: &str) -> PathBuf {
    config::agent_state_dir(username).join("tombstones.json")
}

// ---------- signed DELETE envelope ----------

/// The authenticated body of a DELETE request. Sent as JSON in the
/// HTTP request body. Receiver validates the signature against the
/// sender's known signing pubkey (from `peers.json`) before
/// recording a tombstone and removing the local file.
///
/// Fields that matter for the signature: `user`, `filename`,
/// `deletion_time`. The `source_hostname` and `signer_pubkey` are
/// included for auditability and routing but could in principle be
/// forged without detection — we don't lean on them for authorization.
/// The actual auth check is: did the holder of the private key
/// matching the sending peer's `peers.json` entry sign this envelope?
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeleteEnvelope {
    pub user: String,
    pub filename: String,
    pub deletion_time: i64,
    pub source_hostname: String,
    pub signer_pubkey: String,
    /// Hex-encoded 64-byte Ed25519 signature over
    /// `user|filename|deletion_time` (canonical join — see
    /// [`canonical_sign_bytes`]).
    pub signature: String,
}

/// Canonicalize the bytes that get signed / verified. Separator is
/// a single NUL so unusual filenames (e.g. containing `|`) can't
/// collide with a different `user + filename` combination. Filenames
/// still reject path separators, NULs, and dotfiles elsewhere in the
/// pipeline — defense in depth.
pub fn canonical_sign_bytes(user: &str, filename: &str, deletion_time: i64) -> Vec<u8> {
    let mut out = Vec::with_capacity(user.len() + filename.len() + 16);
    out.extend_from_slice(user.as_bytes());
    out.push(0);
    out.extend_from_slice(filename.as_bytes());
    out.push(0);
    out.extend_from_slice(&deletion_time.to_be_bytes());
    out
}

/// Build a signed DELETE envelope. Signer proves possession of the
/// Ed25519 private key for this envoy; receivers verify against the
/// known pubkey from `peers.json`.
pub fn build_envelope(
    signing_key: &ed25519_dalek::SigningKey,
    source_hostname: &str,
    user: &str,
    filename: &str,
    deletion_time: i64,
) -> DeleteEnvelope {
    let msg = canonical_sign_bytes(user, filename, deletion_time);
    let sig = signing_key.sign(&msg);
    DeleteEnvelope {
        user: user.to_string(),
        filename: filename.to_string(),
        deletion_time,
        source_hostname: source_hostname.to_string(),
        signer_pubkey: crypt::encode_sign_pubkey(&signing_key.verifying_key()),
        signature: hex::encode(sig.to_bytes()),
    }
}

/// Verify a DELETE envelope against the expected signing pubkey (looked
/// up from `peers.json` by the sender's hostname on the receiving
/// side). Returns Ok(()) if the signature checks out, an error
/// otherwise.
///
/// The `expected_pubkey` argument is authoritative — we do NOT trust
/// `envelope.signer_pubkey` for verification. The signer_pubkey field
/// is for humans / logs only. A malicious peer could set it to
/// anything; the receiver uses the peer-table entry.
pub fn verify_envelope(envelope: &DeleteEnvelope, expected_pubkey: &str) -> Result<()> {
    let vk = crypt::decode_sign_pubkey(expected_pubkey)?;
    let sig_bytes = hex::decode(&envelope.signature).context("hex-decoding signature")?;
    if sig_bytes.len() != crypt::SIGN_SIGNATURE_LEN {
        bail!(
            "signature is {} bytes, expected {}",
            sig_bytes.len(),
            crypt::SIGN_SIGNATURE_LEN
        );
    }
    let mut sig_arr = [0u8; 64];
    sig_arr.copy_from_slice(&sig_bytes);
    let sig = ed25519_dalek::Signature::from_bytes(&sig_arr);

    let msg = canonical_sign_bytes(&envelope.user, &envelope.filename, envelope.deletion_time);
    vk.verify(&msg, &sig)
        .map_err(|e| anyhow::anyhow!("signature verification failed: {}", e))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn t(dt: i64) -> Tombstone {
        Tombstone {
            deletion_time: dt,
            source_hostname: "host-a".to_string(),
        }
    }

    #[test]
    fn upsert_new_returns_true() {
        let mut tt = TombstoneTable::empty("dan");
        assert!(tt.upsert("foo.pdf", t(100)));
    }

    #[test]
    fn upsert_older_is_dropped() {
        let mut tt = TombstoneTable::empty("dan");
        tt.upsert("foo.pdf", t(200));
        // Older deletion_time arrives; winner is the existing newer.
        assert!(!tt.upsert("foo.pdf", t(100)));
        assert_eq!(tt.active_deletion_time("foo.pdf"), Some(200));
    }

    #[test]
    fn upsert_same_time_is_noop() {
        let mut tt = TombstoneTable::empty("dan");
        tt.upsert("foo.pdf", t(100));
        assert!(!tt.upsert("foo.pdf", t(100)));
    }

    #[test]
    fn upsert_newer_replaces() {
        let mut tt = TombstoneTable::empty("dan");
        tt.upsert("foo.pdf", t(100));
        assert!(tt.upsert("foo.pdf", t(200)));
        assert_eq!(tt.active_deletion_time("foo.pdf"), Some(200));
    }

    #[test]
    fn gc_drops_expired() {
        // gc keeps tombstones with deletion_time >= now - retention_secs.
        // With now=20_000 and retention=5_000 the cutoff is 15_000, so
        // new.pdf must sit at or above 15_000 to survive — old.pdf at
        // 100 is far past expiry and drops.
        let mut tt = TombstoneTable::empty("dan");
        tt.upsert("old.pdf", t(100));
        tt.upsert("new.pdf", t(18_000));
        let dropped = tt.gc(/* now */ 20_000, /* retention */ 5_000);
        assert_eq!(dropped, 1);
        assert!(tt.active_deletion_time("old.pdf").is_none());
        assert!(tt.active_deletion_time("new.pdf").is_some());
    }

    #[test]
    fn digest_is_stable_under_insertion_order() {
        let mut a = TombstoneTable::empty("dan");
        a.upsert("alpha.pdf", t(100));
        a.upsert("beta.pdf", t(200));
        a.upsert("gamma.pdf", t(300));

        let mut b = TombstoneTable::empty("dan");
        b.upsert("gamma.pdf", t(300));
        b.upsert("alpha.pdf", t(100));
        b.upsert("beta.pdf", t(200));

        assert_eq!(a.digest(), b.digest());
    }

    #[test]
    fn digest_differs_on_content_change() {
        let mut a = TombstoneTable::empty("dan");
        a.upsert("foo.pdf", t(100));
        let d0 = a.digest();
        a.upsert("foo.pdf", t(200));
        assert_ne!(d0, a.digest());
    }

    #[test]
    fn merge_adopts_newer_entries() {
        let mut a = TombstoneTable::empty("dan");
        a.upsert("foo.pdf", t(100));
        a.upsert("bar.pdf", t(100));

        let mut b = TombstoneTable::empty("dan");
        b.upsert("bar.pdf", t(500)); // newer — should win
        b.upsert("baz.pdf", t(100)); // new — should be added

        assert!(a.merge(&b));
        assert_eq!(a.active_deletion_time("foo.pdf"), Some(100));
        assert_eq!(a.active_deletion_time("bar.pdf"), Some(500));
        assert_eq!(a.active_deletion_time("baz.pdf"), Some(100));
    }

    #[test]
    fn remove_clears_when_file_republished() {
        let mut tt = TombstoneTable::empty("dan");
        tt.upsert("foo.pdf", t(100));
        assert!(tt.remove("foo.pdf"));
        assert!(tt.active_deletion_time("foo.pdf").is_none());
        // Double-remove is fine.
        assert!(!tt.remove("foo.pdf"));
    }

    #[test]
    fn envelope_roundtrip_verifies_ok() {
        let key = crypt::generate_signing_keypair();
        let pubkey_text = crypt::encode_sign_pubkey(&key.verifying_key());
        let env = build_envelope(&key, "host-a", "dan", "foo.pdf", 12345);
        verify_envelope(&env, &pubkey_text).unwrap();
    }

    #[test]
    fn envelope_rejects_wrong_key() {
        let key_a = crypt::generate_signing_keypair();
        let key_b = crypt::generate_signing_keypair();
        let env = build_envelope(&key_a, "host-a", "dan", "foo.pdf", 12345);
        // Verify with B's pubkey — should fail.
        let wrong = crypt::encode_sign_pubkey(&key_b.verifying_key());
        assert!(verify_envelope(&env, &wrong).is_err());
    }

    #[test]
    fn envelope_rejects_tampered_field() {
        let key = crypt::generate_signing_keypair();
        let pubkey_text = crypt::encode_sign_pubkey(&key.verifying_key());
        let mut env = build_envelope(&key, "host-a", "dan", "foo.pdf", 12345);
        // Flip the filename post-sign.
        env.filename = "evil.pdf".to_string();
        assert!(verify_envelope(&env, &pubkey_text).is_err());
    }

    #[test]
    fn envelope_rejects_tampered_time() {
        let key = crypt::generate_signing_keypair();
        let pubkey_text = crypt::encode_sign_pubkey(&key.verifying_key());
        let mut env = build_envelope(&key, "host-a", "dan", "foo.pdf", 12345);
        env.deletion_time = 99999;
        assert!(verify_envelope(&env, &pubkey_text).is_err());
    }
}
