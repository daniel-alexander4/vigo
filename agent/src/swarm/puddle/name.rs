// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Friendly puddle-name claims (ADR-022).
//!
//! A **name claim** binds a human-meaningful name (`dan`, `acme`) to a
//! puddle pubkey, so `gitback://dan/vigo` can be used alongside the
//! self-certifying hex form `gitback://<project_id>/vigo`. Resolution:
//! `<name>` → puddle pubkey (via this claim), then
//! `project_id = sha256(<that pubkey> ‖ <repo-handle>)`, then proceed
//! exactly as a hex URL would.
//!
//! A claim is a small signed blob — `{ puddle_pubkey, name, claimed_at,
//! supersedes?, sig }` — signed by the puddle key over a
//! domain-separated canonical encoding ([`NAME_CLAIM_DOMAIN`]),
//! hand-rolled like [`crate::swarm::gitback::tokens`] /
//! [`crate::swarm::gitback::envelopes`] (never JSON whitespace).
//! Persisted at `~<user>/.vigo-puddle/name-claim.json`. Creating one
//! requires the puddle unlocked (the session helper signs).
//!
//! Claims propagate **as traits**: the `gitback` traits collector
//! reads each user's `name-claim.json` and reports it; `gitbackmesh`
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
use crate::swarm::gitback::envelopes::DelegationEnvelope;

/// Length of a raw Ed25519 puddle pubkey.
pub const PUBKEY_LEN: usize = 32;

/// Length of an Ed25519 signature.
pub const SIG_LEN: usize = 64;

/// Domain-separator prefix for the canonical bytes signed in a name
/// claim. Bumped if the claim shape ever changes. A receiver rejects a
/// signature over a different domain even with matching key material —
/// keeps a name-claim signature from being confused with a token or a
/// delegation envelope.
pub const NAME_CLAIM_DOMAIN: &[u8] = b"gitback-name-v1\0";

/// Max length of a puddle name. Matches the gitback project-handle
/// limit so `<name>/<handle>` URLs stay bounded.
pub const MAX_NAME_LEN: usize = 64;

/// Validate a puddle name. Same character rules as gitback project
/// handles (see `gitback::cli::validate_handle`): 1-64 chars, ASCII
/// alphanumeric + `-` `_` `.`, no leading dot, no whitespace.
pub fn validate_name(name: &str) -> Result<()> {
    if name.is_empty() || name.len() > MAX_NAME_LEN {
        bail!("puddle name must be 1-{} characters", MAX_NAME_LEN);
    }
    if name.starts_with('.') {
        bail!("puddle name must not start with '.'");
    }
    // A name that looks exactly like a project_id (64 lowercase hex)
    // would always be parsed as the hex form in a `gitback://` URL,
    // so a friendly URL using it could never resolve. Reject up front.
    if name.len() == 64 && name.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)) {
        bail!("puddle name must not look like a project_id (64 lowercase hex characters)");
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
    /// resolving `gitback://<name>/<handle>`.
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

/// Outcome of resolving a `<puddle-name>` to a puddle pubkey from the
/// set of competing claims for that name (ADR-022).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum NameResolve {
    /// Exactly one claimant — resolve to it.
    Unique([u8; PUBKEY_LEN]),
    /// Several claimants, but one supersedes all the others via a
    /// verified rekey-delegation chain (a `re-found` mid-propagation)
    /// — resolve to it.
    Superseded([u8; PUBKEY_LEN]),
    /// No claimant for this name.
    NotFound,
    /// Several unrelated claimants — ambiguous; the caller must fall
    /// back to `gitback://<project_id>/<repo>`. Carries the claimant
    /// pubkeys (hex) for the error message.
    Ambiguous(Vec<String>),
}

/// Resolve `<name>` from `claims` — the set of **signature-verified**
/// claims for that name (the caller drops claims whose signature
/// doesn't check before calling this; `verify_signature` is *not*
/// re-run here). An empty slice → [`NameResolve::NotFound`]. With more
/// than one claim, walks each claim's `supersedes` chain: a claim
/// whose verified chain descends from every other claimant supersedes
/// them (this is what makes `re-found` seamless across the
/// propagation window); otherwise the name is ambiguous.
pub fn resolve_claims(claims: &[NameClaim]) -> NameResolve {
    match claims.len() {
        0 => NameResolve::NotFound,
        1 => match claims[0].puddle_pubkey_bytes() {
            Ok(pk) => NameResolve::Unique(pk),
            Err(_) => NameResolve::Ambiguous(vec![claims[0].body.puddle_pubkey.clone()]),
        },
        _ => {
            let mut pubkeys: Vec<[u8; PUBKEY_LEN]> = Vec::with_capacity(claims.len());
            for c in claims {
                match c.puddle_pubkey_bytes() {
                    Ok(pk) => pubkeys.push(pk),
                    Err(_) => {
                        return NameResolve::Ambiguous(
                            claims.iter().map(|c| c.body.puddle_pubkey.clone()).collect(),
                        )
                    }
                }
            }
            for (i, c) in claims.iter().enumerate() {
                let this_pk = pubkeys[i];
                let Some(ancestry) = verified_chain_ancestry(&c.body.supersedes, &this_pk) else {
                    continue;
                };
                let absorbs_all_others = pubkeys
                    .iter()
                    .enumerate()
                    .all(|(j, pk)| j == i || ancestry.contains(pk));
                if absorbs_all_others {
                    return NameResolve::Superseded(this_pk);
                }
            }
            NameResolve::Ambiguous(claims.iter().map(|c| c.body.puddle_pubkey.clone()).collect())
        }
    }
}

/// Verify a `supersedes` delegation chain ends at `terminal_pubkey`:
/// each link's signature is valid (over its embedded
/// `old_founder_pubkey`), consecutive links chain
/// (`new` of link _i_ == `old` of link _i+1_), and the last link's
/// `new_founder_pubkey` equals `terminal_pubkey`. On success returns
/// the set of every `old_founder_pubkey` in the chain — the ancestry
/// `terminal_pubkey` descends from. `None` for an empty, malformed,
/// mis-linked, or bad-signature chain.
fn verified_chain_ancestry(
    chain: &[DelegationEnvelope],
    terminal_pubkey: &[u8; PUBKEY_LEN],
) -> Option<std::collections::HashSet<[u8; PUBKEY_LEN]>> {
    if chain.is_empty() {
        return None;
    }
    let mut ancestry = std::collections::HashSet::new();
    let mut expect_old: Option<[u8; PUBKEY_LEN]> = None;
    for env in chain {
        let raw = env.verify_signature().ok()?;
        if let Some(exp) = expect_old {
            if raw.old_founder_pubkey != exp {
                return None;
            }
        }
        ancestry.insert(raw.old_founder_pubkey);
        expect_old = Some(raw.new_founder_pubkey);
    }
    if expect_old.as_ref() == Some(terminal_pubkey) {
        Some(ancestry)
    } else {
        None
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
    fn validate_name_rejects_project_id_lookalike() {
        let pid_like = "a".repeat(64);
        assert!(validate_name(&pid_like).is_err());
        // 63 hex chars is fine (not project_id-shaped).
        assert!(validate_name(&"a".repeat(63)).is_ok());
        // 64 chars with a non-hex char is fine.
        assert!(validate_name(&("z".to_string() + &"a".repeat(63))).is_ok());
    }

    #[test]
    fn resolve_claims_handles_zero_one_and_ambiguous() {
        assert_eq!(resolve_claims(&[]), NameResolve::NotFound);

        let a = fresh_key();
        let claim_a = NameClaim::issue(&a, body_for(&a, "dan")).unwrap();
        assert_eq!(
            resolve_claims(std::slice::from_ref(&claim_a)),
            NameResolve::Unique(a.verifying_key().to_bytes())
        );

        let b = fresh_key();
        let claim_b = NameClaim::issue(&b, body_for(&b, "dan")).unwrap();
        match resolve_claims(&[claim_a.clone(), claim_b.clone()]) {
            NameResolve::Ambiguous(pks) => assert_eq!(pks.len(), 2),
            other => panic!("expected Ambiguous, got {:?}", other),
        }
    }

    #[test]
    fn resolve_claims_follows_supersedes_chain() {
        // genesis A → rekeyed B. B's claim carries supersedes [A→B].
        let a = fresh_key();
        let b = fresh_key();
        let pid = [9u8; 32]; // the old project's id; irrelevant beyond sig coverage
        let d_ab = DelegationEnvelope::build(&a, &pid, &b.verifying_key().to_bytes(), 100);
        let mut body_b = body_for(&b, "dan");
        body_b.supersedes = vec![d_ab.clone()];
        let claim_b = NameClaim::issue(&b, body_b).unwrap();
        let claim_a = NameClaim::issue(&a, body_for(&a, "dan")).unwrap();

        // A and B compete; B supersedes A.
        assert_eq!(
            resolve_claims(&[claim_a.clone(), claim_b.clone()]),
            NameResolve::Superseded(b.verifying_key().to_bytes())
        );
        // Just B → unique.
        assert_eq!(
            resolve_claims(std::slice::from_ref(&claim_b)),
            NameResolve::Unique(b.verifying_key().to_bytes())
        );

        // Two-link chain: A → B → C. C's claim carries [A→B, B→C].
        let c = fresh_key();
        let d_bc = DelegationEnvelope::build(&b, &pid, &c.verifying_key().to_bytes(), 200);
        let mut body_c = body_for(&c, "dan");
        body_c.supersedes = vec![d_ab.clone(), d_bc.clone()];
        let claim_c = NameClaim::issue(&c, body_c).unwrap();
        assert_eq!(
            resolve_claims(&[claim_a.clone(), claim_b.clone(), claim_c.clone()]),
            NameResolve::Superseded(c.verifying_key().to_bytes())
        );

        // A claim with a chain that doesn't actually reach its own pubkey
        // is ignored — A and a bogus B' stay ambiguous.
        let bprime = fresh_key();
        let mut body_bp = body_for(&bprime, "dan");
        body_bp.supersedes = vec![d_ab.clone()]; // chain ends at B, not B'
        let claim_bp = NameClaim::issue(&bprime, body_bp).unwrap();
        match resolve_claims(&[claim_a, claim_bp]) {
            NameResolve::Ambiguous(_) => {}
            other => panic!("expected Ambiguous (mis-targeted chain ignored), got {:?}", other),
        }
    }

    #[test]
    fn resolve_claims_rejects_unsigned_chain_link() {
        // B's claim carries a delegation that was never actually signed
        // by A (tampered signature) — the chain must not be honored.
        let a = fresh_key();
        let b = fresh_key();
        let pid = [3u8; 32];
        let mut forged = DelegationEnvelope::build(&a, &pid, &b.verifying_key().to_bytes(), 100);
        forged.signature = hex::encode([0u8; 64]);
        let mut body_b = body_for(&b, "dan");
        body_b.supersedes = vec![forged];
        let claim_b = NameClaim::issue(&b, body_b).unwrap();
        let claim_a = NameClaim::issue(&a, body_for(&a, "dan")).unwrap();
        match resolve_claims(&[claim_a, claim_b]) {
            NameResolve::Ambiguous(_) => {}
            other => panic!("expected Ambiguous (forged chain link), got {:?}", other),
        }
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
