// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Lockbox membership LEAVE records — the authoritative log of "which
//! age pubkeys are no longer recipients for this user."
//!
//! A LEAVE envelope is a signed, self-only declaration that an age
//! identity is leaving the circle. Unlike DELETE tombstones (which
//! govern individual ciphertext files), LEAVE records govern
//! membership: peers drop the evicted pubkey from their `peers.json`
//! and regenerate `recipients.txt` excluding it.
//!
//! Design mirrors `tombstones.rs`:
//!
//! 1. **Keyed by the subject's age pubkey, not hostname.** A
//!    re-enrolled host under the same hostname generates a fresh age
//!    identity; the old LEAVE targets only the old pubkey and cannot
//!    evict the reborn host.
//! 2. **Idempotent.** Receiving the same LEAVE twice is a no-op; the
//!    record is re-asserted with the same `leave_time`.
//! 3. **Digest-gossip catch-up.** Announcements carry a 16-byte
//!    `leaves_digest`; peers that observe a mismatch pull the full
//!    list via `GET /lockbox/leaves/<user>` and merge.
//!
//! ### Trust model
//!
//! The signer MUST be the subject. `verify_envelope` looks up the
//! signing pubkey registered for `hostname_leaving` in the receiver's
//! `peers.json`; if the signer's key doesn't match, the envelope is
//! rejected. This enforces **self-leave only** for the P2P path —
//! cross-member eviction via envelope is not possible. An admin path
//! (TaskDispatch from the server) exists separately, authenticated by
//! mTLS at the control plane level, not by this envelope.
//!
//! ### Retention
//!
//! Records age out at 90 days (same default as tombstones). A new
//! enrollment for the same hostname with a fresh pubkey is unaffected
//! — the old LEAVE targets the old pubkey, and pubkeys don't recycle.
//!
//! Persistence: `/var/lib/vigo/swarm/lockbox/<username>/leaves.json`
//! (root-owned 0600). Mirrors `tombstones.json` layout.

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

/// Compile-time fallback retention (90 days). Read sites should
/// prefer [`effective_retention_secs`].
pub const DEFAULT_RETENTION_SECS: i64 = 90 * 24 * 60 * 60;

/// Server-configurable retention from `server.yaml swarm.lockbox.
/// envelope_retention`; falls back to [`DEFAULT_RETENTION_SECS`].
pub fn effective_retention_secs() -> i64 {
    crate::swarm::runtime_limits::lockbox_envelope_retention_secs()
}

/// Length of the on-announcement digest. 16 bytes (128 bits) gives
/// negligible collision probability across realistic membership
/// churn.
pub const DIGEST_LEN: usize = 16;

/// A single LEAVE record. `leave_time` is unix seconds at the moment
/// the envelope was signed. `hostname_leaving` is retained for audit
/// and UI rendering but is NOT the key — peers key on pubkey.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct LeaveRecord {
    pub leave_time: i64,
    pub hostname_leaving: String,
    /// Hex Ed25519 signature of the originating LEAVE envelope by
    /// `hostname_leaving`'s daemon key, kept so catch-up peers can verify
    /// the record ([`LeavesTable::verified_advances`]). Empty for a leave
    /// recorded without one (operator-driven foreign evict): applies
    /// locally, accepted by no peer.
    #[serde(default)]
    pub signature: String,
}

/// Per-user leave table. Persisted at
/// `/var/lib/vigo/swarm/lockbox/<user>/leaves.json`.
///
/// BTreeMap (rather than HashMap) so the serialized form is
/// deterministic — required for the on-announcement digest to be
/// stable across envoys that built the same logical set in different
/// insertion orders.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct LeavesTable {
    pub user: String,
    pub leaves: BTreeMap<String, LeaveRecord>,
}

impl LeavesTable {
    pub fn empty(user: &str) -> Self {
        Self {
            user: user.to_string(),
            leaves: BTreeMap::new(),
        }
    }

    /// Load from disk, or return empty on missing/malformed. Never
    /// errors — absence is the common case on first boot. Malformed
    /// content is logged, discarded, and the empty replacement is
    /// written back so subsequent loads stop re-parsing the same
    /// broken file. Mirrors the self-heal in `TombstoneTable::load`.
    pub fn load(user: &str) -> Self {
        let path = leaves_path(user);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_else(|e| {
                tracing::warn!(
                    path = %path.display(),
                    error = %e,
                    "lockbox: malformed leaves.json, starting fresh and overwriting"
                );
                let empty = Self::empty(user);
                if let Err(save_err) = empty.save() {
                    tracing::warn!(
                        path = %path.display(),
                        error = %save_err,
                        "lockbox: failed to overwrite malformed leaves.json; will re-warn next load"
                    );
                }
                empty
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// CLI-facing load that reads the user-side mirror at
    /// `~/.lockbox/leaves.json` instead of the daemon-only canonical
    /// path. Used by `vigo swarm lockbox status` / future leaves verbs running
    /// as the unprivileged user. Same fall-through semantics as `load`.
    pub fn load_user(user: &str, home: &std::path::Path) -> Self {
        let path = config::leaves_user_path(home);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_else(|e| {
                tracing::warn!(
                    path = %path.display(),
                    error = %e,
                    "lockbox: malformed user-mirror leaves.json, starting fresh"
                );
                Self::empty(user)
            }),
            Err(_) => Self::empty(user),
        }
    }

    /// Atomic save via temp + rename. Canonical state is root-owned
    /// 0600 at `agent_state_dir/<user>/leaves.json`. After the
    /// canonical write succeeds, a best-effort mirror is also written
    /// at `~<user>/.lockbox/leaves.json` (user-owned, 0600) so user-side
    /// CLI verbs can read it without sudo.
    pub fn save(&self) -> Result<()> {
        let dir = config::agent_state_dir(&self.user);
        fs::create_dir_all(&dir).with_context(|| format!("creating {}", dir.display()))?;
        let path = leaves_path(&self.user);
        let tmp = path.with_extension("json.tmp");
        let json = serde_json::to_string_pretty(self).context("serializing leaves table")?;

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
        super::write_user_mirror_json(&self.user, "leaves.json", &json);
        Ok(())
    }

    /// Atomic load → mutate → save under the per-user leaves lock.
    /// Mirrors `TombstoneTable::modify`; same lost-update race
    /// applies if multiple LEAVE envelopes for the same user land
    /// concurrently. Closure returns `true` to trigger save().
    pub fn modify<F>(user: &str, f: F) -> Result<()>
    where
        F: FnOnce(&mut Self) -> bool,
    {
        let lock = super::locks::user_lock(super::locks::LockTable::Leaves, user);
        let _guard = lock.lock().unwrap_or_else(|p| p.into_inner());
        let mut table = Self::load(user);
        if f(&mut table) {
            table.save()?;
        }
        Ok(())
    }

    /// Insert-or-update a LEAVE. Later `leave_time` wins. Returns
    /// true if this call changed the stored record (new key OR
    /// leave_time advanced); callers can skip an unnecessary
    /// `save()` when false.
    pub fn upsert(&mut self, age_pubkey: &str, record: LeaveRecord) -> bool {
        match self.leaves.get(age_pubkey) {
            Some(existing) if existing.leave_time >= record.leave_time => false,
            _ => {
                self.leaves.insert(age_pubkey.to_string(), record);
                true
            }
        }
    }

    /// Is `age_pubkey` currently marked as having left? Returns its
    /// `leave_time` if so, else None.
    pub fn leave_time_for(&self, age_pubkey: &str) -> Option<i64> {
        self.leaves.get(age_pubkey).map(|r| r.leave_time)
    }

    /// Drop records older than `retention_secs` relative to `now`.
    /// Returns the count dropped; caller should `save()` when > 0.
    pub fn gc(&mut self, now: i64, retention_secs: i64) -> usize {
        let cutoff = now - retention_secs;
        let before = self.leaves.len();
        self.leaves.retain(|_, r| r.leave_time >= cutoff);
        before - self.leaves.len()
    }

    /// 16-byte digest of the sorted `(age_pubkey, leave_time)` pairs.
    /// Piggybacks on announcements so peers can detect divergence
    /// without carrying the full list every 30 s. BTreeMap iteration
    /// is sorted, so order is stable across envoys that hold the
    /// same logical set.
    pub fn digest(&self) -> [u8; DIGEST_LEN] {
        let mut hasher = Sha256::new();
        for (pk, r) in &self.leaves {
            hasher.update(pk.as_bytes());
            hasher.update(b"|");
            hasher.update(r.leave_time.to_be_bytes());
            hasher.update(b"\n");
        }
        let full = hasher.finalize();
        let mut out = [0u8; DIGEST_LEN];
        out.copy_from_slice(&full[..DIGEST_LEN]);
        out
    }

    /// The records in a peer's table that are new or advanced relative to
    /// this one AND carry a LEAVE signature that verifies against the
    /// signing pubkey `sign_pubkey_for` returns for `hostname_leaving` (the
    /// local peer table). Catch-up applies exactly these; an unverifiable
    /// record is dropped, because applying a leave removes a peer from the
    /// recipient set.
    pub fn verified_advances(
        &self,
        other: &LeavesTable,
        sign_pubkey_for: impl Fn(&str) -> Option<String>,
    ) -> Vec<(String, LeaveRecord)> {
        let mut out = Vec::new();
        for (pk, r) in &other.leaves {
            if self.leave_time_for(pk).is_some_and(|prior| prior >= r.leave_time) {
                continue;
            }
            let Some(sign_pk) = sign_pubkey_for(&r.hostname_leaving) else {
                continue;
            };
            if verify_record(&self.user, pk, r, &sign_pk).is_err() {
                tracing::warn!(
                    user = %self.user,
                    hostname_leaving = %r.hostname_leaving,
                    "lockbox: dropping catch-up leave with a bad signature"
                );
                continue;
            }
            out.push((pk.clone(), r.clone()));
        }
        out
    }
}

/// `/var/lib/vigo/swarm/lockbox/<user>/leaves.json`.
pub fn leaves_path(username: &str) -> PathBuf {
    config::leaves_path(username)
}

// ---------- signed LEAVE envelope ----------

/// The authenticated body of a LEAVE request. Sent as JSON in the
/// HTTP request body. Receiver validates the signature against the
/// signing pubkey of `hostname_leaving` in its local `peers.json`
/// before recording the leave and removing the pubkey from its
/// peer table.
///
/// Fields used for the signature: `user`, `age_pubkey_leaving`,
/// `leave_time`. `hostname_leaving` and `signer_sign_pubkey` are
/// included for audit + routing but could in principle be forged
/// without detection — we don't lean on them for authorization.
/// The actual auth check is: did the holder of the private key
/// matching the sending peer's `peers.json` entry sign this envelope?
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LeaveEnvelope {
    pub user: String,
    pub age_pubkey_leaving: String,
    pub hostname_leaving: String,
    pub leave_time: i64,
    pub signer_sign_pubkey: String,
    /// Hex-encoded 64-byte Ed25519 signature over
    /// `user|age_pubkey_leaving|leave_time` (canonical join — see
    /// [`canonical_sign_bytes`]).
    pub signature: String,
}

/// Domain tag for LEAVE signatures, distinct from DELETE / RESURRECT.
pub const LEAVE_DOMAIN: &[u8] = b"vigo-lockbox-leave-v1\0";

/// Canonicalize the bytes that get signed / verified, prefixed with
/// [`LEAVE_DOMAIN`]. Separator is a
/// single NUL so unusual pubkey encodings can't collide with a
/// different `user + pubkey` combination.
pub fn canonical_sign_bytes(user: &str, age_pubkey_leaving: &str, leave_time: i64) -> Vec<u8> {
    let mut out = Vec::with_capacity(LEAVE_DOMAIN.len() + user.len() + age_pubkey_leaving.len() + 16);
    out.extend_from_slice(LEAVE_DOMAIN);
    out.extend_from_slice(user.as_bytes());
    out.push(0);
    out.extend_from_slice(age_pubkey_leaving.as_bytes());
    out.push(0);
    out.extend_from_slice(&leave_time.to_be_bytes());
    out
}

/// Build a signed LEAVE envelope. Signer proves possession of the
/// Ed25519 private key for this envoy's daemon; receivers verify
/// against the known pubkey from `peers.json`.
pub fn build_envelope(
    signing_key: &ed25519_dalek::SigningKey,
    hostname_leaving: &str,
    user: &str,
    age_pubkey_leaving: &str,
    leave_time: i64,
) -> LeaveEnvelope {
    let msg = canonical_sign_bytes(user, age_pubkey_leaving, leave_time);
    let sig = signing_key.sign(&msg);
    LeaveEnvelope {
        user: user.to_string(),
        age_pubkey_leaving: age_pubkey_leaving.to_string(),
        hostname_leaving: hostname_leaving.to_string(),
        leave_time,
        signer_sign_pubkey: crypt::encode_sign_pubkey(&signing_key.verifying_key()),
        signature: hex::encode(sig.to_bytes()),
    }
}

/// Verify a LEAVE envelope against the expected signing pubkey
/// (looked up from `peers.json` by `hostname_leaving` on the
/// receiving side).
///
/// **Self-leave enforcement:** `expected_pubkey` MUST be the signing
/// pubkey registered for `envelope.hostname_leaving` in the
/// receiver's peer table. That ensures only the subject can sign a
/// LEAVE for its own pubkey — a different peer's signing key would
/// fail verification. Cross-member eviction via envelope is
/// cryptographically impossible.
///
/// The `expected_pubkey` argument is authoritative — we do NOT trust
/// `envelope.signer_sign_pubkey` for verification. That field is for
/// humans / logs only. A malicious peer could set it to anything; the
/// receiver uses the peer-table entry.
pub fn verify_envelope(envelope: &LeaveEnvelope, expected_pubkey: &str) -> Result<()> {
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

    let msg = canonical_sign_bytes(
        &envelope.user,
        &envelope.age_pubkey_leaving,
        envelope.leave_time,
    );
    vk.verify(&msg, &sig)
        .map_err(|e| anyhow::anyhow!("signature verification failed: {}", e))?;
    Ok(())
}

/// Verify a stored leave record as the LEAVE envelope it came from.
pub fn verify_record(user: &str, age_pubkey: &str, r: &LeaveRecord, expected_pubkey: &str) -> Result<()> {
    verify_envelope(
        &LeaveEnvelope {
            user: user.to_string(),
            age_pubkey_leaving: age_pubkey.to_string(),
            hostname_leaving: r.hostname_leaving.clone(),
            leave_time: r.leave_time,
            signer_sign_pubkey: String::new(),
            signature: r.signature.clone(),
        },
        expected_pubkey,
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    fn sample_record(t: i64) -> LeaveRecord {
        LeaveRecord {
            leave_time: t,
            hostname_leaving: "host-a".to_string(),
            signature: String::new(),
        }
    }

    #[test]
    fn upsert_new_returns_true() {
        let mut tt = LeavesTable::empty("dan");
        assert!(tt.upsert("age1abc", sample_record(100)));
    }

    #[test]
    fn upsert_older_is_dropped() {
        let mut tt = LeavesTable::empty("dan");
        tt.upsert("age1abc", sample_record(200));
        assert!(!tt.upsert("age1abc", sample_record(100)));
        assert_eq!(tt.leave_time_for("age1abc"), Some(200));
    }

    #[test]
    fn upsert_same_time_is_noop() {
        let mut tt = LeavesTable::empty("dan");
        tt.upsert("age1abc", sample_record(100));
        assert!(!tt.upsert("age1abc", sample_record(100)));
    }

    #[test]
    fn upsert_newer_replaces() {
        let mut tt = LeavesTable::empty("dan");
        tt.upsert("age1abc", sample_record(100));
        assert!(tt.upsert("age1abc", sample_record(200)));
        assert_eq!(tt.leave_time_for("age1abc"), Some(200));
    }

    #[test]
    fn digest_is_stable_across_insertion_orders() {
        let mut a = LeavesTable::empty("dan");
        a.upsert("age1a", sample_record(1));
        a.upsert("age1b", sample_record(2));
        a.upsert("age1c", sample_record(3));

        let mut b = LeavesTable::empty("dan");
        b.upsert("age1c", sample_record(3));
        b.upsert("age1a", sample_record(1));
        b.upsert("age1b", sample_record(2));

        assert_eq!(a.digest(), b.digest());
    }

    #[test]
    fn digest_differs_when_time_advances() {
        let mut a = LeavesTable::empty("dan");
        a.upsert("age1a", sample_record(1));
        let d1 = a.digest();
        a.upsert("age1a", sample_record(2));
        let d2 = a.digest();
        assert_ne!(d1, d2);
    }

    #[test]
    fn gc_drops_expired_records() {
        let mut tt = LeavesTable::empty("dan");
        tt.upsert("fresh", sample_record(9_500));
        tt.upsert("old", sample_record(100));
        // now=10_000, retention=1_000 → cutoff=9_000 → fresh (9500) kept, old (100) dropped.
        let dropped = tt.gc(10_000, 1_000);
        assert_eq!(dropped, 1);
        assert!(tt.leaves.contains_key("fresh"));
        assert!(!tt.leaves.contains_key("old"));
    }

    fn signed_record(sk: &SigningKey, user: &str, age_pk: &str, t: i64) -> LeaveRecord {
        let env = build_envelope(sk, "host-a", user, age_pk, t);
        LeaveRecord { leave_time: t, hostname_leaving: "host-a".into(), signature: env.signature }
    }

    #[test]
    fn verified_advances_takes_newer_and_new_signed_records() {
        let sk = fresh_key();
        let pk = crate::swarm::lockbox::crypt::encode_sign_pubkey(&sk.verifying_key());
        let mut a = LeavesTable::empty("dan");
        a.upsert("age1", sample_record(10));
        a.upsert("age3", sample_record(30));

        let mut b = LeavesTable::empty("dan");
        b.upsert("age1", signed_record(&sk, "dan", "age1", 20)); // newer
        b.upsert("age2", signed_record(&sk, "dan", "age2", 5)); // new
        b.upsert("age3", signed_record(&sk, "dan", "age3", 30)); // not newer

        let mut got = a.verified_advances(&b, |h| (h == "host-a").then(|| pk.clone()));
        got.sort_by(|x, y| x.0.cmp(&y.0));
        let keys: Vec<_> = got.iter().map(|(k, r)| (k.as_str(), r.leave_time)).collect();
        assert_eq!(keys, vec![("age1", 20), ("age2", 5)]);
    }

    /// Catch-up must not let an answering peer evict members: unsigned,
    /// wrong-key, wrong-user and unknown-signer leaves are all dropped.
    #[test]
    fn verified_advances_drops_unverifiable_records() {
        let sk = fresh_key();
        let other = fresh_key();
        let pk = crate::swarm::lockbox::crypt::encode_sign_pubkey(&sk.verifying_key());
        let mut b = LeavesTable::empty("dan");
        b.upsert("unsigned", sample_record(5));
        b.upsert("forged", signed_record(&other, "dan", "forged", 5));
        b.upsert("wronguser", signed_record(&sk, "eve", "wronguser", 5));
        b.upsert("swapped", signed_record(&sk, "dan", "age-original", 5));
        let a = LeavesTable::empty("dan");
        assert!(a.verified_advances(&b, |_| Some(pk.clone())).is_empty());
        let mut ok = LeavesTable::empty("dan");
        ok.upsert("age9", signed_record(&sk, "dan", "age9", 5));
        assert!(a.verified_advances(&ok, |_| None).is_empty(), "unknown signer");
        assert_eq!(a.verified_advances(&ok, |_| Some(pk.clone())).len(), 1);
    }

    // ---------- envelope round-trip ----------

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    #[test]
    fn sign_and_verify_roundtrips() {
        let sk = fresh_key();
        let envelope = build_envelope(&sk, "danlap", "dan", "age1abc", 1_700_000_000);
        let pk_str = crypt::encode_sign_pubkey(&sk.verifying_key());
        assert!(verify_envelope(&envelope, &pk_str).is_ok());
    }

    #[test]
    fn verify_fails_with_wrong_pubkey() {
        let sk_a = fresh_key();
        let sk_b = fresh_key();
        let envelope = build_envelope(&sk_a, "danlap", "dan", "age1abc", 1_700_000_000);
        let other_pk = crypt::encode_sign_pubkey(&sk_b.verifying_key());
        assert!(verify_envelope(&envelope, &other_pk).is_err());
    }

    #[test]
    fn verify_rejects_tampered_pubkey_field() {
        let sk = fresh_key();
        let mut envelope = build_envelope(&sk, "danlap", "dan", "age1abc", 1_700_000_000);
        // attacker swaps in a different subject — signature no longer
        // covers the same canonical bytes.
        envelope.age_pubkey_leaving = "age1xyz".to_string();
        let pk_str = crypt::encode_sign_pubkey(&sk.verifying_key());
        assert!(verify_envelope(&envelope, &pk_str).is_err());
    }

    #[test]
    fn verify_rejects_tampered_time() {
        let sk = fresh_key();
        let mut envelope = build_envelope(&sk, "danlap", "dan", "age1abc", 1_700_000_000);
        envelope.leave_time += 1;
        let pk_str = crypt::encode_sign_pubkey(&sk.verifying_key());
        assert!(verify_envelope(&envelope, &pk_str).is_err());
    }
}
