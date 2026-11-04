// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Lockbox resurrect state — the operator-override store.
//!
//! When a user wants to recreate a filename that's currently
//! tombstoned (within the 90-day retention window), they run
//! `vigo swarm lockbox resurrect <file>`. The verb drops a
//! sentinel; the daemon picks it up, persists a `ResurrectRecord`
//! `{filename, resurrect_time, source_hostname}` in the per-user
//! resurrects store, and fans a signed `ResurrectEnvelope` out to
//! every live peer.
//!
//! The push/pull gates in `server.rs` then consult
//! `tombstones::effective_tombstone(name, &resurrects)` instead of
//! `TombstoneTable::active_deletion_time` directly: a tombstone is
//! "effective" iff it exists AND no resurrect with later time
//! supersedes it. So the resurrect record acts as a deliberate
//! override that wins by `max(time)` per filename.
//!
//! Catch-up: announcements carry a 16-byte `resurrects_digest`
//! truncated SHA-256 over sorted `(filename, resurrect_time)`
//! pairs. Receivers that observe a digest mismatch pull the
//! announcer's full table from
//! `GET /swarm/lockbox/resurrects/<user>` and merge — same
//! pattern as tombstones.
//!
//! Retention: same 90-day default as tombstones. Both ages out
//! together so the gate logic stays coherent — once both have
//! aged out, the filename is freely reusable.
//!
//! Persistence: `/var/lib/vigo/swarm/lockbox/<username>/resurrects.json`
//! (root-owned, 0600) — canonical, daemon-only. After every save
//! the daemon also writes a user-readable mirror at
//! `~<user>/.lockbox/resurrects.json` (user-owned, 0600) so
//! user-context CLI verbs can read the table without sudo.

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

/// Default retention window in seconds (90 days). Same as tombstones —
/// the two tables age out in lockstep so the effective-tombstone gate
/// can't get stuck in a half-resurrected state. Configurable per user
/// via `tombstone_retention_days` in `~/.lockbox/config.yaml` (the
/// resurrects table reuses the tombstone retention knob — there's no
/// case for resurrects to live longer or shorter than the tombstones
/// they supersede). Compile-time fallback for [`effective_retention_secs`].
pub const DEFAULT_RETENTION_SECS: i64 = 90 * 24 * 60 * 60;

/// Server-configurable retention from `server.yaml swarm.lockbox.
/// envelope_retention`; falls back to [`DEFAULT_RETENTION_SECS`].
pub fn effective_retention_secs() -> i64 {
    crate::swarm::runtime_limits::lockbox_envelope_retention_secs()
}

/// Length of the on-announcement digest. 16 bytes (128 bits) — same
/// as tombstones, same collision argument.
pub const DIGEST_LEN: usize = 16;

/// A single resurrect record. `resurrect_time` is unix seconds at
/// the moment the operator ran the verb on the source envoy.
/// `source_hostname` is recorded for audit and is not load-bearing
/// for the gate decision (the signed envelope's signature is the
/// authoritative authenticator).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ResurrectRecord {
    pub resurrect_time: i64,
    pub source_hostname: String,
    /// Hex Ed25519 signature of the originating RESURRECT envelope by
    /// `source_hostname`'s daemon key, kept so catch-up peers can verify
    /// the record ([`ResurrectsTable::merge_verified`]). Empty when this
    /// envoy could not sign it: gates locally, accepted by no peer.
    #[serde(default)]
    pub signature: String,
}

/// Per-user resurrect table. Persisted at
/// `/var/lib/vigo/swarm/lockbox/<user>/resurrects.json`.
///
/// Keyed by filename for O(1) lookup during push/pull gating.
/// BTreeMap rather than HashMap so the serialized form is
/// deterministic — required for the on-announcement digest to be
/// stable across envoys that reconstructed the same logical set in
/// different insertion orders.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ResurrectsTable {
    pub user: String,
    pub resurrects: BTreeMap<String, ResurrectRecord>,
}

impl ResurrectsTable {
    pub fn empty(user: &str) -> Self {
        Self {
            user: user.to_string(),
            resurrects: BTreeMap::new(),
        }
    }

    /// Load from disk, or return empty if the file is missing or
    /// malformed. Mirror of `TombstoneTable::load`'s fall-through
    /// semantics.
    pub fn load(user: &str) -> Self {
        let path = resurrects_path(user);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_else(|e| {
                tracing::warn!(
                    path = %path.display(),
                    error = %e,
                    "lockbox: malformed resurrects.json, starting fresh and overwriting"
                );
                let empty = Self::empty(user);
                if let Err(save_err) = empty.save() {
                    tracing::warn!(
                        path = %path.display(),
                        error = %save_err,
                        "lockbox: failed to overwrite malformed resurrects.json; will re-warn next load"
                    );
                }
                empty
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// CLI-facing load that reads the user-side mirror at
    /// `~/.lockbox/resurrects.json` instead of the daemon-only
    /// canonical path. Used by user-context verbs (e.g.
    /// `cmd_encrypt` preflight, `cmd_resurrect`, `cmd_list`).
    pub fn load_user(user: &str, home: &std::path::Path) -> Self {
        let path = config::resurrects_user_path(home);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_else(|e| {
                tracing::warn!(
                    path = %path.display(),
                    error = %e,
                    "lockbox: malformed user-mirror resurrects.json, starting fresh"
                );
                Self::empty(user)
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// Atomic save via temp + rename. Canonical state is root-owned
    /// 0600 at `agent_state_dir/<user>/resurrects.json`. After the
    /// canonical write succeeds, a best-effort mirror is also
    /// written at `~<user>/.lockbox/resurrects.json` so
    /// user-context CLI verbs can read it without sudo.
    pub fn save(&self) -> Result<()> {
        let dir = config::agent_state_dir(&self.user);
        fs::create_dir_all(&dir).with_context(|| format!("creating {}", dir.display()))?;
        let path = resurrects_path(&self.user);
        let tmp = path.with_extension("json.tmp");
        let json = serde_json::to_string_pretty(self).context("serializing resurrect table")?;

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
        super::write_user_mirror_json(&self.user, "resurrects.json", &json);
        Ok(())
    }

    /// Atomic load → mutate → save under the per-user resurrects
    /// lock. Mirrors `TombstoneTable::modify`. Closure returns true
    /// iff it mutated; helper only saves when so.
    pub fn modify<F>(user: &str, f: F) -> Result<()>
    where
        F: FnOnce(&mut Self) -> bool,
    {
        let lock = super::locks::user_lock(super::locks::LockTable::Resurrects, user);
        let _guard = lock.lock().unwrap_or_else(|p| p.into_inner());
        let mut table = Self::load(user);
        if f(&mut table) {
            table.save()?;
        }
        Ok(())
    }

    /// Insert-or-update a resurrect record. Later `resurrect_time`
    /// wins when two records race. Returns true if this call
    /// changed the stored record (new entry OR resurrect_time
    /// advanced).
    pub fn upsert(&mut self, filename: &str, r: ResurrectRecord) -> bool {
        match self.resurrects.get(filename) {
            Some(existing) if existing.resurrect_time >= r.resurrect_time => false,
            _ => {
                self.resurrects.insert(filename.to_string(), r);
                true
            }
        }
    }

    /// Remove a resurrect record. Used during GC. Returns true if
    /// the entry was present.
    pub fn remove(&mut self, filename: &str) -> bool {
        self.resurrects.remove(filename).is_some()
    }

    /// Active resurrect record for `filename`, if any.
    pub fn active(&self, filename: &str) -> Option<&ResurrectRecord> {
        self.resurrects.get(filename)
    }

    /// Drop resurrects older than `retention_secs` relative to
    /// `now`. Returns the count dropped; caller should `save()`
    /// when > 0.
    pub fn gc(&mut self, now: i64, retention_secs: i64) -> usize {
        let cutoff = now - retention_secs;
        let before = self.resurrects.len();
        self.resurrects.retain(|_, r| r.resurrect_time >= cutoff);
        before - self.resurrects.len()
    }

    /// 16-byte digest of the sorted `(filename, resurrect_time)`
    /// pairs. Piggybacks on announcements so peers can detect
    /// divergence without carrying the full list every cycle.
    pub fn digest(&self) -> [u8; DIGEST_LEN] {
        let mut hasher = Sha256::new();
        for (name, r) in &self.resurrects {
            hasher.update(name.as_bytes());
            hasher.update(b"|");
            hasher.update(r.resurrect_time.to_be_bytes());
            hasher.update(b"\n");
        }
        let full = hasher.finalize();
        let mut out = [0u8; DIGEST_LEN];
        out.copy_from_slice(&full[..DIGEST_LEN]);
        out
    }

    /// Merge a peer's table, accepting only records whose RESURRECT
    /// signature verifies against the pubkey `sign_pubkey_for` returns for
    /// the record's `source_hostname` (the local peer table). Unverifiable
    /// records are dropped — an unsigned resurrect would let any peer undo
    /// a delete. Later `resurrect_time` wins per filename. Returns true if
    /// any entry was added or advanced — caller should `save()` when so.
    pub fn merge_verified(
        &mut self,
        other: &ResurrectsTable,
        sign_pubkey_for: impl Fn(&str) -> Option<String>,
    ) -> bool {
        let mut changed = false;
        for (name, r) in &other.resurrects {
            if self.active(name).is_some_and(|cur| cur.resurrect_time >= r.resurrect_time) {
                continue;
            }
            let Some(pk) = sign_pubkey_for(&r.source_hostname) else {
                continue;
            };
            if verify_record(&self.user, name, r, &pk).is_err() {
                tracing::warn!(
                    user = %self.user,
                    filename = %name,
                    signer = %r.source_hostname,
                    "lockbox: dropping catch-up resurrect with a bad signature"
                );
                continue;
            }
            if self.upsert(name, r.clone()) {
                changed = true;
            }
        }
        changed
    }
}

/// `/var/lib/vigo/swarm/lockbox/<user>/resurrects.json`.
pub fn resurrects_path(username: &str) -> PathBuf {
    config::agent_state_dir(username).join("resurrects.json")
}

// ---------- signed RESURRECT envelope ----------

/// The authenticated body of a RESURRECT request. Sent as JSON in
/// the HTTP request body. Receiver validates the signature against
/// the sender's known signing pubkey (from `peers.json`) before
/// recording the resurrect locally — same auth shape as
/// `tombstones::DeleteEnvelope`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResurrectEnvelope {
    pub user: String,
    pub filename: String,
    pub resurrect_time: i64,
    pub source_hostname: String,
    pub signer_pubkey: String,
    /// Hex-encoded 64-byte Ed25519 signature over
    /// `user|filename|resurrect_time` (canonical join — see
    /// [`canonical_sign_bytes`]).
    pub signature: String,
}

/// Domain tag for RESURRECT signatures — distinct from
/// [`super::tombstones::DELETE_DOMAIN`] so neither replays as the other.
pub const RESURRECT_DOMAIN: &[u8] = b"vigo-lockbox-resurrect-v1\0";

/// Canonicalize the bytes that get signed / verified, prefixed with
/// [`RESURRECT_DOMAIN`]. Mirror of
/// `tombstones::canonical_sign_bytes` — single NUL separator, fields
/// in fixed order.
pub fn canonical_sign_bytes(user: &str, filename: &str, resurrect_time: i64) -> Vec<u8> {
    let mut out = Vec::with_capacity(RESURRECT_DOMAIN.len() + user.len() + filename.len() + 16);
    out.extend_from_slice(RESURRECT_DOMAIN);
    out.extend_from_slice(user.as_bytes());
    out.push(0);
    out.extend_from_slice(filename.as_bytes());
    out.push(0);
    out.extend_from_slice(&resurrect_time.to_be_bytes());
    out
}

/// Build a signed RESURRECT envelope. Mirror of
/// `tombstones::build_envelope`.
pub fn build_envelope(
    signing_key: &ed25519_dalek::SigningKey,
    source_hostname: &str,
    user: &str,
    filename: &str,
    resurrect_time: i64,
) -> ResurrectEnvelope {
    let msg = canonical_sign_bytes(user, filename, resurrect_time);
    let sig = signing_key.sign(&msg);
    ResurrectEnvelope {
        user: user.to_string(),
        filename: filename.to_string(),
        resurrect_time,
        source_hostname: source_hostname.to_string(),
        signer_pubkey: crypt::encode_sign_pubkey(&signing_key.verifying_key()),
        signature: hex::encode(sig.to_bytes()),
    }
}

/// Verify a RESURRECT envelope against the expected signing
/// pubkey. Mirror of `tombstones::verify_envelope`. Returns Ok(())
/// if the signature checks out, an error otherwise.
pub fn verify_envelope(envelope: &ResurrectEnvelope, expected_pubkey: &str) -> Result<()> {
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

    let msg = canonical_sign_bytes(&envelope.user, &envelope.filename, envelope.resurrect_time);
    vk.verify(&msg, &sig)
        .map_err(|e| anyhow::anyhow!("signature verification failed: {}", e))?;
    Ok(())
}

/// Verify a stored resurrect record as the RESURRECT envelope it came from.
pub fn verify_record(user: &str, filename: &str, r: &ResurrectRecord, expected_pubkey: &str) -> Result<()> {
    verify_envelope(
        &ResurrectEnvelope {
            user: user.to_string(),
            filename: filename.to_string(),
            resurrect_time: r.resurrect_time,
            source_hostname: r.source_hostname.clone(),
            signer_pubkey: String::new(),
            signature: r.signature.clone(),
        },
        expected_pubkey,
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    fn r(rt: i64) -> ResurrectRecord {
        ResurrectRecord {
            resurrect_time: rt,
            source_hostname: "host-a".to_string(),
            signature: String::new(),
        }
    }

    #[test]
    fn upsert_new_returns_true() {
        let mut tt = ResurrectsTable::empty("dan");
        assert!(tt.upsert("foo.pdf", r(100)));
    }

    #[test]
    fn upsert_older_is_dropped() {
        let mut tt = ResurrectsTable::empty("dan");
        tt.upsert("foo.pdf", r(200));
        assert!(!tt.upsert("foo.pdf", r(100)));
        assert_eq!(tt.active("foo.pdf").unwrap().resurrect_time, 200);
    }

    #[test]
    fn upsert_newer_replaces() {
        let mut tt = ResurrectsTable::empty("dan");
        tt.upsert("foo.pdf", r(100));
        assert!(tt.upsert("foo.pdf", r(200)));
        assert_eq!(tt.active("foo.pdf").unwrap().resurrect_time, 200);
    }

    #[test]
    fn gc_drops_expired() {
        let mut tt = ResurrectsTable::empty("dan");
        tt.upsert("old.pdf", r(100));
        tt.upsert("new.pdf", r(18_000));
        let dropped = tt.gc(20_000, 5_000);
        assert_eq!(dropped, 1);
        assert!(tt.active("old.pdf").is_none());
        assert!(tt.active("new.pdf").is_some());
    }

    #[test]
    fn digest_is_stable_under_insertion_order() {
        let mut a = ResurrectsTable::empty("dan");
        a.upsert("alpha.pdf", r(100));
        a.upsert("beta.pdf", r(200));
        a.upsert("gamma.pdf", r(300));

        let mut b = ResurrectsTable::empty("dan");
        b.upsert("gamma.pdf", r(300));
        b.upsert("alpha.pdf", r(100));
        b.upsert("beta.pdf", r(200));

        assert_eq!(a.digest(), b.digest());
    }

    #[test]
    fn digest_differs_on_content_change() {
        let mut a = ResurrectsTable::empty("dan");
        a.upsert("foo.pdf", r(100));
        let d0 = a.digest();
        a.upsert("foo.pdf", r(200));
        assert_ne!(d0, a.digest());
    }

    fn signed(key: &ed25519_dalek::SigningKey, user: &str, name: &str, rt: i64) -> ResurrectRecord {
        let env = build_envelope(key, "host-a", user, name, rt);
        ResurrectRecord { resurrect_time: rt, source_hostname: "host-a".into(), signature: env.signature }
    }

    /// Catch-up resurrects from a peer are accepted only with a valid
    /// RESURRECT signature by a signer in the local peer table.
    #[test]
    fn merge_verified_drops_unverifiable_records() {
        let key = crypt::generate_signing_keypair();
        let pk = crypt::encode_sign_pubkey(&key.verifying_key());
        let mut remote = ResurrectsTable::empty("dan");
        remote.upsert("unsigned.pdf", r(100));
        remote.upsert("wronguser.pdf", signed(&key, "eve", "wronguser.pdf", 100));
        let del = super::super::tombstones::build_envelope(&key, "host-a", "dan", "replayed.pdf", 100);
        remote.upsert(
            "replayed.pdf",
            ResurrectRecord { resurrect_time: 100, source_hostname: "host-a".into(), signature: del.signature },
        );
        let mut local = ResurrectsTable::empty("dan");
        assert!(!local.merge_verified(&remote, |_| Some(pk.clone())));
        assert!(local.resurrects.is_empty(), "{:?}", local.resurrects);
        let mut unknown = ResurrectsTable::empty("dan");
        unknown.upsert("ok.pdf", signed(&key, "dan", "ok.pdf", 100));
        assert!(!local.merge_verified(&unknown, |_| None));
    }

    #[test]
    fn merge_adopts_newer_entries() {
        let key = crypt::generate_signing_keypair();
        let pk = crypt::encode_sign_pubkey(&key.verifying_key());
        let mut a = ResurrectsTable::empty("dan");
        a.upsert("foo.pdf", r(100));
        a.upsert("bar.pdf", r(100));

        let mut b = ResurrectsTable::empty("dan");
        b.upsert("bar.pdf", signed(&key, "dan", "bar.pdf", 500));
        b.upsert("baz.pdf", signed(&key, "dan", "baz.pdf", 100));

        assert!(a.merge_verified(&b, |_| Some(pk.clone())));
        assert_eq!(a.active("foo.pdf").unwrap().resurrect_time, 100);
        assert_eq!(a.active("bar.pdf").unwrap().resurrect_time, 500);
        assert_eq!(a.active("baz.pdf").unwrap().resurrect_time, 100);
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
        let wrong = crypt::encode_sign_pubkey(&key_b.verifying_key());
        assert!(verify_envelope(&env, &wrong).is_err());
    }

    #[test]
    fn envelope_rejects_tampered_field() {
        let key = crypt::generate_signing_keypair();
        let pubkey_text = crypt::encode_sign_pubkey(&key.verifying_key());
        let mut env = build_envelope(&key, "host-a", "dan", "foo.pdf", 12345);
        env.filename = "evil.pdf".to_string();
        assert!(verify_envelope(&env, &pubkey_text).is_err());
    }

    #[test]
    fn envelope_rejects_tampered_time() {
        let key = crypt::generate_signing_keypair();
        let pubkey_text = crypt::encode_sign_pubkey(&key.verifying_key());
        let mut env = build_envelope(&key, "host-a", "dan", "foo.pdf", 12345);
        env.resurrect_time = 99999;
        assert!(verify_envelope(&env, &pubkey_text).is_err());
    }
}
