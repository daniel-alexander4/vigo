// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Friendly puddle-name claims (ADR-022).
//!
//! A **name claim** binds a human-meaningful name (`dan`, `acme`) to a
//! puddle pubkey, so `swarmgit://dan/vigo` can be used alongside the
//! self-certifying hex form `swarmgit://<project_id>/vigo`. Resolution:
//! `<name>` → puddle pubkey (via this claim), then
//! `project_id = sha256(<that pubkey> ‖ <repo-handle>)`, then proceed
//! exactly as a hex URL would.
//!
//! A claim is a small signed blob — `{ puddle_pubkey, name, claimed_at,
//! supersedes?, sig }` — signed by the puddle key over a
//! domain-separated canonical encoding ([`NAME_CLAIM_DOMAIN`]),
//! hand-rolled like [`crate::swarm::swarmgit::tokens`] /
//! [`crate::swarm::swarmgit::envelopes`] (never JSON whitespace).
//! Persisted at `~<user>/.vigo-puddle/name-claim.json`. Creating one
//! requires the puddle unlocked (the session helper signs).
//!
//! Claims propagate **as traits**: the `swarmgit` traits collector
//! reads each user's `name-claim.json` and reports it; `swarmgitmesh`
//! joins them into a fleet `name → pubkey` map; the server publishes
//! it at `GET /api/v1/swarm/puddle/names`. The server **publishes,
//! never arbitrates** — collisions degrade to *ambiguous* (a resolver
//! errors and tells the operator to use the hex form), never to
//! *wrong*.
//!
//! **`puddle_pubkey`** is the local `identity.pub` at claim time —
//! correct for the overwhelmingly common case (a puddle that has
//! never rekeyed: current == genesis, and projects are anchored to
//! it). A bare puddle rekey (ADR-018) does **not** touch the claim
//! (the friendly URL keeps resolving to the pre-rekey projects); only
//! `re-found` (ADR-019 slice 5) re-issues it, with `supersedes`
//! carrying the rekey-delegation chain that proves the new anchor
//! descends from the old. See ADR-022 for the full design + the
//! Zooko-triangle tradeoffs (friendly names are a convenience, the
//! hex pid is the proof).

use std::path::Path;

use anyhow::{anyhow, bail, Context, Result};
use ed25519_dalek::{Signature, Signer, SigningKey, Verifier, VerifyingKey};
use serde::{Deserialize, Serialize};

use super::config;
use crate::swarm::swarmgit::envelopes::DelegationEnvelope;

/// Length of a raw Ed25519 puddle pubkey.
pub const PUBKEY_LEN: usize = 32;

/// Length of an Ed25519 signature.
pub const SIG_LEN: usize = 64;

/// Domain-separator prefix for the canonical bytes signed in a name
/// claim. Bumped if the claim shape ever changes. A receiver rejects a
/// signature over a different domain even with matching key material —
/// keeps a name-claim signature from being confused with a token or a
/// delegation envelope.
pub const NAME_CLAIM_DOMAIN: &[u8] = b"swarmgit-name-v1\0";

/// Max length of a puddle name. Matches the swarmgit project-handle
/// limit so `<name>/<handle>` URLs stay bounded.
pub const MAX_NAME_LEN: usize = 64;

/// Validate a puddle name. Same character rules as swarmgit project
/// handles (see `swarmgit::cli::validate_handle`): 1-64 chars, ASCII
/// alphanumeric + `-` `_` `.`, no leading dot, no whitespace.
pub fn validate_name(name: &str) -> Result<()> {
    if name.is_empty() || name.len() > MAX_NAME_LEN {
        bail!("puddle name must be 1-{} characters", MAX_NAME_LEN);
    }
    if name.starts_with('.') {
        bail!("puddle name must not start with '.'");
    }
    for c in name.chars() {
        if !(c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.') {
            bail!(
                "puddle name '{}' contains invalid character '{}'; \
                 use ASCII alphanumeric + '-' '_' '.'",
                name,
                c
            );
        }
    }
    Ok(())
}

/// Body of a name claim — the part the puddle key signs.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct NameClaimBody {
    /// Hex-encoded puddle pubkey claiming the name. This is the pubkey
    /// `project_id = sha256(pubkey ‖ handle)` is computed against when
    /// resolving `swarmgit://<name>/<handle>`.
    pub puddle_pubkey: String,
    /// The friendly name being claimed. `validate_name`-checked.
    pub name: String,
    /// Unix seconds when the claim was signed.
    pub claimed_at: i64,
    /// Rekey-delegation chain (ADR-019 type, reused) proving this
    /// pubkey descends from a previous puddle pubkey that held the same
    /// name. Empty for a fresh first claim; populated by `re-found`.
    /// A resolver seeing `name → A` and `name → B` where B's claim
    /// chains back to A treats it as "B supersedes A" — not a
    /// collision.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub supersedes: Vec<DelegationEnvelope>,
}

/// A signed name claim: body + Ed25519 signature over the canonical
/// bytes of that body.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct NameClaim {
    pub body: NameClaimBody,
    /// Hex-encoded 64-byte Ed25519 signature.
    pub signature: String,
}

/// Hand-rolled deterministic encoding for [`NameClaimBody`]. NEVER mix
/// this with serde — JSON whitespace and field-order ambiguity would
/// silently produce two different signed messages for what looks like
/// the same body. Layout:
///
/// 1. [`NAME_CLAIM_DOMAIN`]
/// 2. `puddle_pubkey` (32 B)
/// 3. `name` length (u16 BE) ‖ `name` UTF-8 bytes
/// 4. `claimed_at` (8 B BE i64)
/// 5. `supersedes` count (u16 BE), then for each delegation:
///    `project_id`(32) ‖ `old_founder_pubkey`(32) ‖
///    `new_founder_pubkey`(32) ‖ `delegated_at`(8 BE) ‖ `signature`(64)
///
/// Fallible because the embedded delegation envelopes carry hex
/// fields that may be malformed; the caller surfaces that as a bad
/// claim rather than panicking.
pub fn canonical_bytes(body: &NameClaimBody) -> Result<Vec<u8>> {
    let puddle_pubkey = parse_pubkey_hex(&body.puddle_pubkey).context("name claim puddle_pubkey")?;
    let name_bytes = body.name.as_bytes();
    if name_bytes.len() > u16::MAX as usize {
        bail!("name too long for canonical encoding");
    }
    if body.supersedes.len() > u16::MAX as usize {
        bail!("supersedes chain too long for canonical encoding");
    }
    let mut out = Vec::with_capacity(
        NAME_CLAIM_DOMAIN.len() + 32 + 2 + name_bytes.len() + 8 + 2 + body.supersedes.len() * 168,
    );
    out.extend_from_slice(NAME_CLAIM_DOMAIN);
    out.extend_from_slice(&puddle_pubkey);
    out.extend_from_slice(&(name_bytes.len() as u16).to_be_bytes());
    out.extend_from_slice(name_bytes);
    out.extend_from_slice(&body.claimed_at.to_be_bytes());
    out.extend_from_slice(&(body.supersedes.len() as u16).to_be_bytes());
    for env in &body.supersedes {
        let raw = env.raw().context("supersedes delegation envelope")?;
        out.extend_from_slice(&raw.project_id);
        out.extend_from_slice(&raw.old_founder_pubkey);
        out.extend_from_slice(&raw.new_founder_pubkey);
        out.extend_from_slice(&env.delegated_at.to_be_bytes());
        out.extend_from_slice(&raw.signature);
    }
    Ok(out)
}

impl NameClaim {
    /// Issue + sign a name claim with a [`SigningKey`] held in process.
    /// The body's `puddle_pubkey` MUST equal the public half of
    /// `signing_key`; we re-check so a misuse fails loudly here rather
    /// than at verification time on another envoy.
    pub fn issue(signing_key: &SigningKey, body: NameClaimBody) -> Result<NameClaim> {
        let expect = hex::encode(signing_key.verifying_key().to_bytes());
        if body.puddle_pubkey.to_ascii_lowercase() != expect {
            bail!(
                "name claim body.puddle_pubkey ({}) does not match the signing key's public half ({})",
                body.puddle_pubkey,
                expect
            );
        }
        let msg = canonical_bytes(&body)?;
        let sig = signing_key.sign(&msg);
        let claim = NameClaim {
            body,
            signature: hex::encode(sig.to_bytes()),
        };
        claim
            .verify_signature()
            .context("freshly-built name claim failed self-verification")?;
        Ok(claim)
    }

    /// Issue + sign with a delegated signer — used by the CLI, which
    /// holds the key only in the puddle session helper. `sign` receives
    /// the canonical bytes and returns the raw 64-byte signature.
    /// Self-verifies before returning so a bad signature fails loudly.
    pub fn issue_with<F>(body: NameClaimBody, sign: F) -> Result<NameClaim>
    where
        F: FnOnce(&[u8]) -> Result<[u8; SIG_LEN]>,
    {
        let msg = canonical_bytes(&body)?;
        let sig = sign(&msg)?;
        let claim = NameClaim {
            body,
            signature: hex::encode(sig),
        };
        claim
            .verify_signature()
            .context("freshly-built name claim failed self-verification")?;
        Ok(claim)
    }

    /// Verify the Ed25519 signature on this claim against
    /// `body.puddle_pubkey` (the embedded claimant). Does NOT verify
    /// the `supersedes` chain integrity — a resolver does that
    /// separately when reconciling competing claims for the same name.
    pub fn verify_signature(&self) -> Result<()> {
        let pubkey = parse_pubkey_hex(&self.body.puddle_pubkey).context("name claim puddle_pubkey")?;
        let vk = VerifyingKey::from_bytes(&pubkey)
            .map_err(|e| anyhow!("invalid puddle pubkey: {}", e))?;
        let sig_bytes = hex::decode(&self.signature).context("name claim signature hex")?;
        if sig_bytes.len() != SIG_LEN {
            bail!(
                "name claim signature is {} bytes, expected {}",
                sig_bytes.len(),
                SIG_LEN
            );
        }
        let mut sig_arr = [0u8; SIG_LEN];
        sig_arr.copy_from_slice(&sig_bytes);
        let sig = Signature::from_bytes(&sig_arr);
        let msg = canonical_bytes(&self.body)?;
        vk.verify(&msg, &sig)
            .map_err(|e| anyhow!("name claim signature verification failed: {}", e))?;
        Ok(())
    }

    /// The claimant pubkey as raw bytes.
    pub fn puddle_pubkey_bytes(&self) -> Result<[u8; PUBKEY_LEN]> {
        parse_pubkey_hex(&self.body.puddle_pubkey)
    }

    // ---------- persistence ----------

    /// Read the name claim for the user whose home is `home`, if any.
    /// `Ok(None)` when no claim exists; `Err` on a present-but-corrupt
    /// file (the caller surfaces it rather than silently treating it as
    /// "no claim").
    pub fn load(home: &Path) -> Result<Option<NameClaim>> {
        let path = config::name_claim_path(home);
        match std::fs::read_to_string(&path) {
            Ok(s) => {
                let claim: NameClaim = serde_json::from_str(&s)
                    .with_context(|| format!("parsing {}", path.display()))?;
                Ok(Some(claim))
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(e).with_context(|| format!("reading {}", path.display())),
        }
    }

    /// Atomically write this claim to `~<user>/.vigo-puddle/name-claim.json`
    /// (0644 — the contents are public-by-construction). `username` is
    /// the owning user; the file is chowned to it when running as root.
    pub fn save(&self, home: &Path, username: &str) -> Result<()> {
        let path = config::name_claim_path(home);
        let body = serde_json::to_vec_pretty(self).context("serializing name claim")?;
        super::cli::write_user_file_0644(&path, &body, username)
            .with_context(|| format!("writing {}", path.display()))
    }

    /// Remove the name claim for this user, if present. Idempotent.
    pub fn clear(home: &Path) -> Result<()> {
        let path = config::name_claim_path(home);
        match std::fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(e).with_context(|| format!("removing {}", path.display())),
        }
    }
}

/// Parse a 64-char lowercase-hex Ed25519 pubkey (the canonical form
/// in name claims and project state). Accepts an optional `ed25519:`
/// prefix so callers can pass `identity.pub` contents directly.
pub fn parse_pubkey_hex(s: &str) -> Result<[u8; PUBKEY_LEN]> {
    let hex_str = s.strip_prefix("ed25519:").unwrap_or(s).trim();
    let bytes = hex::decode(hex_str).context("pubkey hex")?;
    if bytes.len() != PUBKEY_LEN {
        bail!("pubkey is {} bytes, expected {}", bytes.len(), PUBKEY_LEN);
    }
    let mut out = [0u8; PUBKEY_LEN];
    out.copy_from_slice(&bytes);
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn body_for(key: &SigningKey, name: &str) -> NameClaimBody {
        NameClaimBody {
            puddle_pubkey: hex::encode(key.verifying_key().to_bytes()),
            name: name.to_string(),
            claimed_at: 1_700_000_000,
            supersedes: vec![],
        }
    }

    #[test]
    fn validate_name_accepts_normal() {
        for n in ["dan", "acme-corp", "team_42", "a.b.c", "x"] {
            assert!(validate_name(n).is_ok(), "{} should be valid", n);
        }
    }

    #[test]
    fn validate_name_rejects_bad() {
        assert!(validate_name("").is_err());
        assert!(validate_name(".hidden").is_err());
        assert!(validate_name("has space").is_err());
        assert!(validate_name("slash/in/name").is_err());
        assert!(validate_name(&"x".repeat(65)).is_err());
    }

    #[test]
    fn issue_and_verify_roundtrip() {
        let key = fresh_key();
        let claim = NameClaim::issue(&key, body_for(&key, "dan")).unwrap();
        claim.verify_signature().unwrap();
        assert_eq!(claim.body.name, "dan");
        assert_eq!(
            claim.puddle_pubkey_bytes().unwrap(),
            key.verifying_key().to_bytes()
        );
    }

    #[test]
    fn issue_rejects_pubkey_mismatch() {
        let signer = fresh_key();
        let other = fresh_key();
        let body = body_for(&other, "dan");
        let err = NameClaim::issue(&signer, body).unwrap_err();
        assert!(err.to_string().contains("does not match"));
    }

    #[test]
    fn tampered_name_fails_verification() {
        let key = fresh_key();
        let mut claim = NameClaim::issue(&key, body_for(&key, "dan")).unwrap();
        claim.body.name = "eve".to_string();
        assert!(claim.verify_signature().is_err());
    }

    #[test]
    fn tampered_pubkey_fails_verification() {
        let key = fresh_key();
        let other = fresh_key();
        let mut claim = NameClaim::issue(&key, body_for(&key, "dan")).unwrap();
        claim.body.puddle_pubkey = hex::encode(other.verifying_key().to_bytes());
        assert!(claim.verify_signature().is_err());
    }

    #[test]
    fn issue_with_delegated_signer_matches_in_process() {
        let key = fresh_key();
        let body = body_for(&key, "dan");
        let direct = NameClaim::issue(&key, body.clone()).unwrap();
        let delegated = NameClaim::issue_with(body, |msg| Ok(key.sign(msg).to_bytes())).unwrap();
        assert_eq!(direct, delegated);
    }

    #[test]
    fn canonical_bytes_start_with_domain() {
        let key = fresh_key();
        let bytes = canonical_bytes(&body_for(&key, "dan")).unwrap();
        assert!(bytes.starts_with(NAME_CLAIM_DOMAIN));
    }

    #[test]
    fn canonical_bytes_are_byte_stable() {
        // Pin the exact layout so an accidental reordering trips here.
        let key_bytes = [7u8; PUBKEY_LEN];
        let body = NameClaimBody {
            puddle_pubkey: hex::encode(key_bytes),
            name: "ab".to_string(),
            claimed_at: 0x0102_0304_0506_0708,
            supersedes: vec![],
        };
        let bytes = canonical_bytes(&body).unwrap();
        let mut expected = Vec::new();
        expected.extend_from_slice(NAME_CLAIM_DOMAIN);
        expected.extend_from_slice(&[7u8; PUBKEY_LEN]);
        expected.extend_from_slice(&2u16.to_be_bytes());
        expected.extend_from_slice(b"ab");
        expected.extend_from_slice(&0x0102_0304_0506_0708i64.to_be_bytes());
        expected.extend_from_slice(&0u16.to_be_bytes());
        assert_eq!(bytes, expected);
    }

    #[test]
    fn supersedes_chain_is_covered_by_signature() {
        // A claim with a supersedes chain verifies; swapping the chain
        // for a different one breaks the signature.
        let key = fresh_key();
        let old = fresh_key();
        let pid = [1u8; 32];
        let env = DelegationEnvelope::build(&old, &pid, &key.verifying_key().to_bytes(), 500);
        let mut body = body_for(&key, "dan");
        body.supersedes = vec![env];
        let claim = NameClaim::issue(&key, body).unwrap();
        claim.verify_signature().unwrap();

        // Swap in a chain link with a different delegated_at.
        let mut tampered = claim.clone();
        let other_env = DelegationEnvelope::build(&old, &pid, &key.verifying_key().to_bytes(), 999);
        tampered.body.supersedes = vec![other_env];
        assert!(tampered.verify_signature().is_err());
    }

    #[test]
    fn parse_pubkey_hex_accepts_ed25519_prefix() {
        let raw = [0xABu8; PUBKEY_LEN];
        let with_prefix = format!("ed25519:{}", hex::encode(raw));
        assert_eq!(parse_pubkey_hex(&with_prefix).unwrap(), raw);
        assert_eq!(parse_pubkey_hex(&hex::encode(raw)).unwrap(), raw);
    }

    #[test]
    fn parse_pubkey_hex_rejects_short() {
        assert!(parse_pubkey_hex("deadbeef").is_err());
    }
}
