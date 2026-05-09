// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Puddle membership certificate for lockbox (ADR-016 follow-up).
//!
//! Closes the same-LAN first-contact race that the slice-2 TOFU pin
//! has on its own: an attacker who beats the real envoy to the first
//! announce wins the pin without a cert; with a cert, only the
//! holder of the puddle's private key can mint a verifiable claim.
//!
//! ### Shape
//!
//! ```text
//! PuddleCert {
//!     version:           1
//!     envoy_age_pubkey:  "age1..."          (the lockbox identity for this envoy)
//!     puddle_pubkey:      "<64-hex>"          (the user's puddle Ed25519 pubkey)
//!     issued_at:         <unix seconds>
//!     expires_at:        <unix seconds>
//!     signature:         "<128-hex>"         (Ed25519 over canonical bytes)
//! }
//! ```
//!
//! ### Issuance
//!
//! Issued by the user's `lockbox puddle init` (or `renew-cert`)
//! verb. Signing happens via the puddle session helper —
//! `request_sign(canonical_bytes)` — which holds the unwrapped
//! Ed25519 key in RAM. The cert lands at
//! `~<user>/.lockbox/puddle_cert.json`, owned by the user, mode 0600.
//!
//! ### Lifetime
//!
//! Default 30 days. The user re-issues at `unlock` time (or any
//! `lockbox puddle renew-cert` call). Expired certs are rejected by
//! receivers — an offline envoy whose cert lapses needs an unlock
//! pass before peers will accept its puddle claim again. The
//! tradeoff: stricter cryptographic posture vs. a periodic ceremony.
//! 30 days is generous enough for normal operation; tunable later if
//! needed.
//!
//! ### Verification (receive-side)
//!
//! 1. `signature` verifies against `puddle_pubkey` over canonical bytes.
//! 2. `envoy_age_pubkey` matches the `Announcement.pubkey`.
//! 3. `puddle_pubkey` matches the `Announcement.puddle_pubkey`.
//! 4. `expires_at >= now`.
//!
//! Any check failure → the puddle claim is dropped (announce treated
//! as if `puddle_pubkey` were empty — peer slots into standalone
//! filtering). The daemon warn-logs the rejection so operators can
//! detect a stale cert before it confuses recipient resolution.

use anyhow::{anyhow, bail, Context, Result};
use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use serde::{Deserialize, Serialize};

/// Domain-separator prefix for canonical signed bytes. Distinct from
/// every other Ed25519 envelope shape vigo signs (swarmgit tokens,
/// swarmgit revoke / leave envelopes, lockbox DELETE / LEAVE).
pub const CERT_DOMAIN: &[u8] = b"vigo-lockbox-puddle-cert-v1\0";

/// On-disk schema version. Bumps when the canonical bytes change.
pub const CERT_VERSION: u32 = 1;

/// Default certificate lifetime: 30 days.
pub const DEFAULT_LIFETIME_SECS: i64 = 30 * 24 * 60 * 60;

/// Length of an Ed25519 signature. Same as the swarmgit tokens
/// constant; redeclared here to avoid the cross-subsystem dep.
pub const SIG_LEN: usize = 64;

/// Puddle certificate as stored on disk and embedded in announces.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct PuddleCert {
    #[serde(default = "default_version")]
    pub version: u32,
    pub envoy_age_pubkey: String,
    pub puddle_pubkey: String,
    pub issued_at: i64,
    pub expires_at: i64,
    pub signature: String,
}

fn default_version() -> u32 {
    CERT_VERSION
}

/// Compute the canonical bytes that get signed. Hand-rolled so a
/// future serde-format drift cannot retroactively change the
/// signature scope. Length-prefix the variable-length string fields
/// to prevent boundary-drift forgery.
pub fn canonical_sign_bytes(
    envoy_age_pubkey: &str,
    puddle_pubkey: &str,
    issued_at: i64,
    expires_at: i64,
) -> Vec<u8> {
    let age = envoy_age_pubkey.as_bytes();
    let puddle_bytes = puddle_pubkey.as_bytes();
    let mut out =
        Vec::with_capacity(CERT_DOMAIN.len() + 4 + age.len() + 4 + puddle_bytes.len() + 8 + 8);
    out.extend_from_slice(CERT_DOMAIN);
    out.extend_from_slice(&(age.len() as u32).to_be_bytes());
    out.extend_from_slice(age);
    out.extend_from_slice(&(puddle_bytes.len() as u32).to_be_bytes());
    out.extend_from_slice(puddle_bytes);
    out.extend_from_slice(&issued_at.to_be_bytes());
    out.extend_from_slice(&expires_at.to_be_bytes());
    out
}

impl PuddleCert {
    /// Build + sign a cert. The signing happens through a closure so
    /// the caller can route to the puddle session helper without
    /// pulling that dependency into this module.
    ///
    /// Returns an Err if the resulting cert fails self-verification —
    /// a defensive guard against a misbehaving signer (e.g. wrong
    /// pubkey paired with right key).
    pub fn issue<F>(
        envoy_age_pubkey: &str,
        puddle_pubkey: &str,
        issued_at: i64,
        lifetime_secs: i64,
        sign: F,
    ) -> Result<Self>
    where
        F: FnOnce(&[u8]) -> Result<[u8; SIG_LEN]>,
    {
        if envoy_age_pubkey.is_empty() {
            bail!("envoy_age_pubkey must not be empty");
        }
        if puddle_pubkey.is_empty() {
            bail!("puddle_pubkey must not be empty");
        }
        if lifetime_secs <= 0 {
            bail!("lifetime_secs must be positive, got {}", lifetime_secs);
        }
        let expires_at = issued_at.saturating_add(lifetime_secs);
        let msg = canonical_sign_bytes(envoy_age_pubkey, puddle_pubkey, issued_at, expires_at);
        let sig = sign(&msg)?;
        let cert = Self {
            version: CERT_VERSION,
            envoy_age_pubkey: envoy_age_pubkey.to_string(),
            puddle_pubkey: puddle_pubkey.to_string(),
            issued_at,
            expires_at,
            signature: hex::encode(sig),
        };
        cert.verify_signature()
            .context("freshly-issued cert failed self-verification")?;
        Ok(cert)
    }

    /// Verify the Ed25519 signature on this cert against
    /// `self.puddle_pubkey`. Does NOT check expiry (callers do that
    /// separately so they can distinguish "tampered" from "stale" in
    /// log messages).
    pub fn verify_signature(&self) -> Result<()> {
        let fleet_bytes = decode_puddle_pubkey(&self.puddle_pubkey)?;
        let vk = VerifyingKey::from_bytes(&fleet_bytes)
            .map_err(|e| anyhow!("malformed puddle pubkey: {}", e))?;
        let sig_bytes = hex::decode(&self.signature).context("hex-decode signature")?;
        if sig_bytes.len() != SIG_LEN {
            bail!(
                "signature is {} bytes, expected {}",
                sig_bytes.len(),
                SIG_LEN
            );
        }
        let mut sig_arr = [0u8; SIG_LEN];
        sig_arr.copy_from_slice(&sig_bytes);
        let sig = Signature::from_bytes(&sig_arr);
        let msg = canonical_sign_bytes(
            &self.envoy_age_pubkey,
            &self.puddle_pubkey,
            self.issued_at,
            self.expires_at,
        );
        vk.verify(&msg, &sig)
            .map_err(|e| anyhow!("signature verification failed: {}", e))
    }

    /// Combined verify-and-validate against a target context: the
    /// cert was signed by the puddle_bytes, hasn't expired, and matches the
    /// envoy_age_pubkey + puddle_pubkey the announce claimed. Returns
    /// the specific [`CertCheck`] reason on failure so callers can
    /// log meaningful details.
    pub fn verify_for_announce(
        &self,
        expected_age_pubkey: &str,
        expected_puddle_pubkey: &str,
        now_secs: i64,
    ) -> std::result::Result<(), CertCheck> {
        if self.envoy_age_pubkey != expected_age_pubkey {
            return Err(CertCheck::AgePubkeyMismatch);
        }
        if self.puddle_pubkey != expected_puddle_pubkey {
            return Err(CertCheck::FleetPubkeyMismatch);
        }
        if now_secs > self.expires_at {
            return Err(CertCheck::Expired {
                expires_at: self.expires_at,
                now: now_secs,
            });
        }
        if now_secs < self.issued_at.saturating_sub(60) {
            // 60 s of clock skew tolerance on the not-yet-valid edge.
            return Err(CertCheck::NotYetValid {
                issued_at: self.issued_at,
                now: now_secs,
            });
        }
        self.verify_signature().map_err(CertCheck::BadSignature)
    }

    /// Days until expiry. Negative if already expired. Surfaced by
    /// `lockbox status` so operators can spot a renewal coming up.
    pub fn days_until_expiry(&self, now_secs: i64) -> i64 {
        let secs = self.expires_at.saturating_sub(now_secs);
        secs / (24 * 60 * 60)
    }
}

/// Why a cert failed `verify_for_announce`. Carries enough context
/// to log a useful warn line without exposing the raw signature.
#[derive(Debug)]
pub enum CertCheck {
    AgePubkeyMismatch,
    FleetPubkeyMismatch,
    Expired { expires_at: i64, now: i64 },
    NotYetValid { issued_at: i64, now: i64 },
    BadSignature(anyhow::Error),
}

impl std::fmt::Display for CertCheck {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CertCheck::AgePubkeyMismatch => write!(
                f,
                "cert envoy_age_pubkey does not match the announce's age_pubkey"
            ),
            CertCheck::FleetPubkeyMismatch => write!(
                f,
                "cert puddle_pubkey does not match the announce's puddle_pubkey"
            ),
            CertCheck::Expired { expires_at, now } => write!(
                f,
                "cert expired at {} (now={}); peer must re-issue via `lockbox puddle renew-cert`",
                expires_at, now
            ),
            CertCheck::NotYetValid { issued_at, now } => write!(
                f,
                "cert not yet valid (issued_at={}, now={}); clock skew?",
                issued_at, now
            ),
            CertCheck::BadSignature(e) => write!(f, "bad signature: {}", e),
        }
    }
}

/// Strip the `ed25519:` prefix (if present) and decode the 64-hex
/// pubkey to raw 32 bytes.
fn decode_puddle_pubkey(text: &str) -> Result<[u8; 32]> {
    let hex_part = text.strip_prefix("ed25519:").unwrap_or(text);
    if hex_part.len() != 64 {
        bail!(
            "puddle pubkey is {} hex chars, expected 64",
            hex_part.len()
        );
    }
    if hex_part.chars().any(|c| c.is_ascii_uppercase()) {
        bail!("puddle pubkey must be lowercase hex");
    }
    let bytes = hex::decode(hex_part).context("decoding puddle pubkey hex")?;
    let mut out = [0u8; 32];
    out.copy_from_slice(&bytes);
    Ok(out)
}

// ---------- on-disk persistence ----------

/// Path where the cert is persisted: `~<user>/.lockbox/puddle_cert.json`.
pub fn cert_path(home: &std::path::Path) -> std::path::PathBuf {
    super::config::dot_dir(home).join("puddle_cert.json")
}

impl PuddleCert {
    /// Read the cert from disk. Returns `None` when the file doesn't
    /// exist (peer hasn't run `lockbox puddle init` / `renew-cert`)
    /// or is unreadable. JSON parse errors are warn-logged + return
    /// `None` so a corrupt cert doesn't crash the announce loop —
    /// the user re-issues via `renew-cert`.
    pub fn load(home: &std::path::Path) -> Option<Self> {
        let path = cert_path(home);
        match std::fs::read_to_string(&path) {
            Ok(s) => match serde_json::from_str::<PuddleCert>(&s) {
                Ok(c) if c.version == CERT_VERSION => Some(c),
                Ok(c) => {
                    tracing::warn!(
                        path = %path.display(),
                        version = c.version,
                        "lockbox: puddle_cert.json schema version != current; re-run `lockbox puddle renew-cert`"
                    );
                    None
                }
                Err(e) => {
                    tracing::warn!(
                        path = %path.display(),
                        error = %e,
                        "lockbox: malformed puddle_cert.json; re-run `lockbox puddle renew-cert`"
                    );
                    None
                }
            },
            Err(_) => None,
        }
    }

    /// Atomic save via temp + rename. Mode 0600 owned by the calling
    /// user (the CLI verb that issues runs as the user).
    pub fn save(&self, home: &std::path::Path) -> Result<()> {
        use std::io::Write;
        let path = cert_path(home);
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)
                .with_context(|| format!("create {}", parent.display()))?;
        }
        let json = serde_json::to_vec_pretty(self).context("serialize cert")?;
        let tmp = path.with_extension("json.tmp");
        {
            #[cfg(unix)]
            use std::os::unix::fs::OpenOptionsExt;
            let mut opts = std::fs::OpenOptions::new();
            opts.create(true).write(true).truncate(true);
            #[cfg(unix)]
            opts.mode(0o600);
            let mut f = opts
                .open(&tmp)
                .with_context(|| format!("create {}", tmp.display()))?;
            f.write_all(&json)?;
            f.sync_all().ok();
        }
        std::fs::rename(&tmp, &path)
            .with_context(|| format!("rename to {}", path.display()))?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};
    use rand::rngs::OsRng;

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn pub_hex(k: &SigningKey) -> String {
        hex::encode(k.verifying_key().to_bytes())
    }

    fn sign_with(key: &SigningKey) -> impl FnOnce(&[u8]) -> Result<[u8; SIG_LEN]> + '_ {
        move |bytes| {
            let sig = key.sign(bytes);
            Ok(sig.to_bytes())
        }
    }

    #[test]
    fn issue_signs_and_self_verifies() {
        let puddle_bytes = fresh_key();
        let cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            1_700_000_000,
            DEFAULT_LIFETIME_SECS,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        cert.verify_signature().unwrap();
        assert_eq!(cert.envoy_age_pubkey, "age1abc");
        assert_eq!(cert.expires_at, 1_700_000_000 + DEFAULT_LIFETIME_SECS);
    }

    #[test]
    fn issue_rejects_wrong_signing_key() {
        let real = fresh_key();
        let imposter = fresh_key();
        // Build a cert claiming `real`'s pubkey but signed by `imposter`.
        let result = PuddleCert::issue(
            "age1abc",
            &pub_hex(&real),
            1_700_000_000,
            3600,
            sign_with(&imposter),
        );
        assert!(
            result.is_err(),
            "issue must reject when signing key doesn't match the claimed puddle pubkey"
        );
    }

    #[test]
    fn issue_rejects_empty_inputs() {
        let puddle_bytes = fresh_key();
        assert!(PuddleCert::issue(
            "",
            &pub_hex(&puddle_bytes),
            100,
            3600,
            sign_with(&puddle_bytes)
        )
        .is_err());
        assert!(PuddleCert::issue(
            "age1abc",
            "",
            100,
            3600,
            sign_with(&puddle_bytes)
        )
        .is_err());
        assert!(PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            100,
            0,
            sign_with(&puddle_bytes)
        )
        .is_err());
    }

    #[test]
    fn verify_for_announce_accepts_matching() {
        let puddle_bytes = fresh_key();
        let cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            100,
            3600,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        cert.verify_for_announce("age1abc", &pub_hex(&puddle_bytes), 200)
            .unwrap();
    }

    #[test]
    fn verify_for_announce_rejects_age_mismatch() {
        let puddle_bytes = fresh_key();
        let cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            100,
            3600,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        let err = cert
            .verify_for_announce("age1WRONG", &pub_hex(&puddle_bytes), 200)
            .unwrap_err();
        assert!(matches!(err, CertCheck::AgePubkeyMismatch));
    }

    #[test]
    fn verify_for_announce_rejects_fleet_mismatch() {
        let puddle_bytes = fresh_key();
        let other = fresh_key();
        let cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            100,
            3600,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        let err = cert
            .verify_for_announce("age1abc", &pub_hex(&other), 200)
            .unwrap_err();
        assert!(matches!(err, CertCheck::FleetPubkeyMismatch));
    }

    #[test]
    fn verify_for_announce_rejects_expired() {
        let puddle_bytes = fresh_key();
        let cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            100,
            3600,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        let err = cert
            .verify_for_announce("age1abc", &pub_hex(&puddle_bytes), 100 + 3600 + 1)
            .unwrap_err();
        assert!(matches!(err, CertCheck::Expired { .. }));
    }

    #[test]
    fn verify_for_announce_tolerates_60s_skew_on_not_yet_valid_edge() {
        let puddle_bytes = fresh_key();
        let cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            100,
            3600,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        // 30 s before issued_at — within tolerance.
        cert.verify_for_announce("age1abc", &pub_hex(&puddle_bytes), 70)
            .unwrap();
        // 120 s before issued_at — outside tolerance.
        let err = cert
            .verify_for_announce("age1abc", &pub_hex(&puddle_bytes), -20)
            .unwrap_err();
        assert!(matches!(err, CertCheck::NotYetValid { .. }));
    }

    #[test]
    fn verify_for_announce_rejects_tampered_signature() {
        let puddle_bytes = fresh_key();
        let mut cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            100,
            3600,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        // Flip a hex char in the signature.
        let mut bytes: Vec<u8> = cert.signature.bytes().collect();
        bytes[0] = if bytes[0] == b'a' { b'b' } else { b'a' };
        cert.signature = String::from_utf8(bytes).unwrap();
        let err = cert
            .verify_for_announce("age1abc", &pub_hex(&puddle_bytes), 200)
            .unwrap_err();
        assert!(matches!(err, CertCheck::BadSignature(_)));
    }

    #[test]
    fn verify_for_announce_rejects_tampered_expiry() {
        let puddle_bytes = fresh_key();
        let mut cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            100,
            3600,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        // Push expiry forward — would extend cert if accepted.
        cert.expires_at += 1_000_000;
        let err = cert
            .verify_for_announce("age1abc", &pub_hex(&puddle_bytes), 200)
            .unwrap_err();
        // Signature covers expiry; tamper invalidates.
        assert!(matches!(err, CertCheck::BadSignature(_)));
    }

    #[test]
    fn puddle_pubkey_strips_ed25519_prefix() {
        let puddle_bytes = fresh_key();
        let prefixed = format!("ed25519:{}", pub_hex(&puddle_bytes));
        let cert = PuddleCert::issue(
            "age1abc",
            &prefixed,
            100,
            3600,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        // Verifies with the prefixed form (canonical form ignores the prefix).
        cert.verify_for_announce("age1abc", &prefixed, 200)
            .unwrap();
    }

    #[test]
    fn days_until_expiry_arithmetic() {
        let puddle_bytes = fresh_key();
        let cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            0,
            DEFAULT_LIFETIME_SECS,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        assert_eq!(cert.days_until_expiry(0), 30);
        assert_eq!(cert.days_until_expiry(15 * 24 * 60 * 60), 15);
        // After expiry: negative.
        assert!(cert.days_until_expiry(35 * 24 * 60 * 60) < 0);
    }

    #[test]
    fn json_round_trips() {
        let puddle_bytes = fresh_key();
        let cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            100,
            3600,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        let json = serde_json::to_string(&cert).unwrap();
        let parsed: PuddleCert = serde_json::from_str(&json).unwrap();
        assert_eq!(cert, parsed);
        parsed.verify_signature().unwrap();
    }

    #[test]
    fn save_and_load_round_trip() {
        let dir = tempfile::tempdir().unwrap();
        // Need a `.lockbox/` subdir so cert_path resolves correctly.
        std::fs::create_dir_all(dir.path().join(".lockbox")).unwrap();
        let puddle_bytes = fresh_key();
        let cert = PuddleCert::issue(
            "age1abc",
            &pub_hex(&puddle_bytes),
            100,
            3600,
            sign_with(&puddle_bytes),
        )
        .unwrap();
        cert.save(dir.path()).unwrap();
        let loaded = PuddleCert::load(dir.path()).unwrap();
        assert_eq!(loaded, cert);
    }

    #[test]
    fn load_returns_none_when_missing() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(dir.path().join(".lockbox")).unwrap();
        assert!(PuddleCert::load(dir.path()).is_none());
    }

    #[test]
    fn load_returns_none_for_garbage_json() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(dir.path().join(".lockbox")).unwrap();
        std::fs::write(cert_path(dir.path()), b"not json").unwrap();
        assert!(PuddleCert::load(dir.path()).is_none());
    }

    #[test]
    fn canonical_bytes_stable_across_calls() {
        let a = canonical_sign_bytes("age1abc", "puddle_bytes-pk", 100, 200);
        let b = canonical_sign_bytes("age1abc", "puddle_bytes-pk", 100, 200);
        assert_eq!(a, b);
    }

    #[test]
    fn canonical_bytes_carry_domain_separator() {
        let bytes = canonical_sign_bytes("age1abc", "puddle_bytes-pk", 100, 200);
        assert!(bytes.starts_with(CERT_DOMAIN));
    }
}
