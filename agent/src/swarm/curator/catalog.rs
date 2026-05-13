// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! The curator catalog entry — one signed envelope per artifact (ADR-024).
//!
//! An *artifact* is keyed `artifact_id = sha256(founder_puddle_pubkey ‖ name)`.
//! Its catalog entry is a [`CatalogEntry`]: a [`CatalogEntryBody`] (versions ×
//! platforms + a mutable tag map + recipients + the P2P-replica target) plus an
//! Ed25519 signature over the body's deterministic [`canonical_bytes`]. The entry
//! is re-signed (with a higher `issued_at`) on every mutation; last-write-wins.
//! The signature *is* the "writable only by puddle members" enforcement — the
//! aggregator and any verifying envoy reject an entry whose signature doesn't
//! anchor (walking the ADR-019 delegation chain) to the pubkey that
//! `artifact_id == sha256(that_pubkey ‖ name)`. A founder cannot mutate another
//! founder's artifact.
//!
//! Modeled on [`crate::swarm::puddle::name`] (the closest existing signed-envelope
//! shape) and reuses [`crate::swarm::gitback::envelopes::DelegationEnvelope`] for
//! the rekey-bridge chain — see this module's `mod.rs` on the eventual `anchor`
//! extraction that will unify those.

use anyhow::{anyhow, bail, Context, Result};
use ed25519_dalek::{Signature, Signer, SigningKey, Verifier, VerifyingKey};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;

use crate::swarm::gitback::envelopes::{DelegationEnvelope, DELEGATION_MAX_LEN};

/// Length of a raw Ed25519 puddle pubkey / a sha256 (artifact_id, blob_sha).
pub const ID_LEN: usize = 32;

/// Length of an Ed25519 signature.
pub const SIG_LEN: usize = 64;

/// Domain-separator prefix for the canonical bytes a catalog entry signs.
/// Bumped if the entry shape changes. A receiver rejects a signature over a
/// different domain even with matching key material — keeps a catalog-entry
/// signature from being confused with a gitback token, a name claim, or a
/// delegation envelope.
pub const CATALOG_DOMAIN: &[u8] = b"curator-catalog-v1\0";

/// Max artifact-name length. Matches the gitback project-handle / puddle-name
/// limit so `<puddle-name>/<name>:<tag>` references stay bounded.
pub const MAX_NAME_LEN: usize = 64;

/// Validate an artifact name. Same character rules as gitback project handles
/// and puddle names: 1-64 chars, ASCII alphanumeric + `-` `_` `.`, no leading
/// dot, not 64 lowercase hex (which would be ambiguous with the hex
/// `<artifact_id>/<name>` reference form).
pub fn validate_artifact_name(name: &str) -> Result<()> {
    if name.is_empty() || name.len() > MAX_NAME_LEN {
        bail!("artifact name must be 1-{} characters", MAX_NAME_LEN);
    }
    if name.starts_with('.') {
        bail!("artifact name must not start with '.'");
    }
    if name.len() == 64 && name.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)) {
        bail!("artifact name must not look like an artifact_id (64 lowercase hex characters)");
    }
    for c in name.chars() {
        if !(c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.') {
            bail!(
                "artifact name '{}' contains invalid character '{}'; use ASCII alphanumeric + '-' '_' '.'",
                name,
                c
            );
        }
    }
    Ok(())
}

/// `artifact_id = sha256(founder_puddle_pubkey ‖ name)` — frozen across all
/// rekeys (the same shape as `gitback::tokens::project_id_for`).
pub fn artifact_id_for(founder_puddle_pubkey: &[u8; ID_LEN], name: &str) -> [u8; ID_LEN] {
    let mut hasher = Sha256::new();
    hasher.update(founder_puddle_pubkey);
    hasher.update(name.as_bytes());
    let digest = hasher.finalize();
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&digest);
    out
}

fn parse_hex32(s: &str, what: &str) -> Result<[u8; ID_LEN]> {
    let bytes = hex::decode(s).with_context(|| format!("{what}: invalid hex"))?;
    if bytes.len() != ID_LEN {
        bail!("{what}: {} bytes, expected {}", bytes.len(), ID_LEN);
    }
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&bytes);
    Ok(out)
}

/// A `{os, arch}` build of an artifact version — one swarm content-addressed blob.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Platform {
    pub os: String,
    pub arch: String,
    /// Hex-encoded sha256 of the blob.
    pub blob_sha: String,
    pub size: u64,
}

/// One published version of an artifact — its set of per-platform blobs.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ArtifactVersion {
    pub platforms: Vec<Platform>,
}

/// The signed part of a catalog entry. `versions` and `tags` are `BTreeMap` so
/// `canonical_bytes` is deterministic regardless of insertion order.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct CatalogEntryBody {
    /// Schema version of this catalog-entry shape. Starts at 1.
    pub schema_version: u32,
    /// The artifact name (`validate_artifact_name`-checked).
    pub name: String,
    /// Hex-encoded GENESIS founder puddle pubkey — `artifact_id` is anchored to
    /// this, frozen forever.
    pub founder_puddle_pubkey: String,
    /// Hex-encoded CURRENT puddle pubkey that signed this entry. Equals
    /// `founder_puddle_pubkey` until the founder rekeys; thereafter `delegations`
    /// bridges founder → signer (ADR-019).
    pub signing_puddle_pubkey: String,
    /// version-string → its platform list.
    pub versions: BTreeMap<String, ArtifactVersion>,
    /// tag → version-string. Mutable across re-signs.
    pub tags: BTreeMap<String, String>,
    /// `["*"]` (fleet-readable, the default) or a list of hex puddle pubkeys
    /// allowed to materialize/consume this artifact.
    pub recipients: Vec<String>,
    /// Hostname-glob / `key=value` trait filter (the `match_target` dialect)
    /// selecting which swarm-enabled envoys hold the P2P DR replica. `"*"` =
    /// everywhere (the default).
    pub target: String,
    /// Unix milliseconds when this entry was signed — last-write-wins ordering
    /// for the aggregator and the gossip merge.
    pub issued_at: i64,
    /// ADR-019 rekey-bridge chain (reused [`DelegationEnvelope`]), genesis → … →
    /// signer. Empty until the founder rekeys.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub delegations: Vec<DelegationEnvelope>,
}

/// A signed catalog entry: body + Ed25519 signature over [`canonical_bytes`] of
/// that body.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct CatalogEntry {
    pub body: CatalogEntryBody,
    /// Hex-encoded 64-byte Ed25519 signature.
    pub signature: String,
}

fn put_str(out: &mut Vec<u8>, s: &str, what: &str) -> Result<()> {
    let b = s.as_bytes();
    if b.len() > u16::MAX as usize {
        bail!("{what} too long for canonical encoding ({} bytes)", b.len());
    }
    out.extend_from_slice(&(b.len() as u16).to_be_bytes());
    out.extend_from_slice(b);
    Ok(())
}

fn put_count(out: &mut Vec<u8>, n: usize, what: &str) -> Result<()> {
    if n > u16::MAX as usize {
        bail!("{what} count {} exceeds canonical-encoding limit", n);
    }
    out.extend_from_slice(&(n as u16).to_be_bytes());
    Ok(())
}

/// Hand-rolled deterministic encoding for [`CatalogEntryBody`]. NEVER mix this
/// with serde — JSON whitespace and field-order ambiguity would silently produce
/// two different signed messages for what looks like the same body. Layout:
///
/// 1. [`CATALOG_DOMAIN`]
/// 2. `schema_version` (4 B BE)
/// 3. `founder_puddle_pubkey` (32 B)
/// 4. `signing_puddle_pubkey` (32 B)
/// 5. `name` (u16 BE len ‖ UTF-8)
/// 6. `issued_at` (8 B BE i64)
/// 7. `target` (u16 BE len ‖ UTF-8)
/// 8. `recipients` (u16 BE count, then each: u16 BE len ‖ UTF-8)
/// 9. `versions` (u16 BE count; BTreeMap order; each: version-string (u16 len ‖ UTF-8),
///    then platforms (u16 BE count; each: os (u16 len ‖ UTF-8) ‖ arch (u16 len ‖ UTF-8)
///    ‖ blob_sha (32 B) ‖ size (8 B BE)))
/// 10. `tags` (u16 BE count; BTreeMap order; each: tag (u16 len ‖ UTF-8) ‖ version-string (u16 len ‖ UTF-8))
/// 11. `delegations` (u16 BE count; each: project_id (32 B) ‖ old_founder_pubkey (32 B)
///     ‖ new_founder_pubkey (32 B) ‖ delegated_at (8 B BE) ‖ signature (64 B))
pub fn canonical_bytes(body: &CatalogEntryBody) -> Result<Vec<u8>> {
    let founder = parse_hex32(&body.founder_puddle_pubkey, "founder_puddle_pubkey")?;
    let signer = parse_hex32(&body.signing_puddle_pubkey, "signing_puddle_pubkey")?;

    let mut out = Vec::with_capacity(CATALOG_DOMAIN.len() + 4 + 32 + 32 + 64);
    out.extend_from_slice(CATALOG_DOMAIN);
    out.extend_from_slice(&body.schema_version.to_be_bytes());
    out.extend_from_slice(&founder);
    out.extend_from_slice(&signer);
    put_str(&mut out, &body.name, "name")?;
    out.extend_from_slice(&body.issued_at.to_be_bytes());
    put_str(&mut out, &body.target, "target")?;

    put_count(&mut out, body.recipients.len(), "recipients")?;
    for r in &body.recipients {
        put_str(&mut out, r, "recipient")?;
    }

    put_count(&mut out, body.versions.len(), "versions")?;
    for (ver, av) in &body.versions {
        put_str(&mut out, ver, "version string")?;
        put_count(&mut out, av.platforms.len(), "platforms")?;
        for p in &av.platforms {
            put_str(&mut out, &p.os, "platform os")?;
            put_str(&mut out, &p.arch, "platform arch")?;
            out.extend_from_slice(&parse_hex32(&p.blob_sha, "platform blob_sha")?);
            out.extend_from_slice(&p.size.to_be_bytes());
        }
    }

    put_count(&mut out, body.tags.len(), "tags")?;
    for (tag, ver) in &body.tags {
        put_str(&mut out, tag, "tag")?;
        put_str(&mut out, ver, "tag target version")?;
    }

    put_count(&mut out, body.delegations.len(), "delegations")?;
    for env in &body.delegations {
        let raw = env.raw().context("delegation envelope")?;
        out.extend_from_slice(&raw.project_id);
        out.extend_from_slice(&raw.old_founder_pubkey);
        out.extend_from_slice(&raw.new_founder_pubkey);
        out.extend_from_slice(&env.delegated_at.to_be_bytes());
        out.extend_from_slice(&raw.signature);
    }

    Ok(out)
}

impl CatalogEntry {
    /// Issue + sign a catalog entry with a [`SigningKey`] held in process.
    /// `body.signing_puddle_pubkey` MUST equal the signing key's public half;
    /// if `body.delegations` is empty it MUST also equal `body.founder_puddle_pubkey`
    /// (no rekey ⇒ the founder is the signer). Self-verifies before returning.
    pub fn issue(signing_key: &SigningKey, body: CatalogEntryBody) -> Result<CatalogEntry> {
        let expect = hex::encode(signing_key.verifying_key().to_bytes());
        if body.signing_puddle_pubkey.to_ascii_lowercase() != expect {
            bail!(
                "catalog entry signing_puddle_pubkey ({}) does not match the signing key's public half ({})",
                body.signing_puddle_pubkey,
                expect
            );
        }
        Self::issue_with(body, |msg| Ok(signing_key.sign(msg).to_bytes()))
    }

    /// Issue + sign with a delegated signer (e.g. the puddle session helper, used
    /// by `vigo`/`vigocli swarm curator push`). `sign` receives the canonical
    /// bytes and returns the raw 64-byte signature. Self-verifies before returning.
    pub fn issue_with<F>(body: CatalogEntryBody, sign: F) -> Result<CatalogEntry>
    where
        F: FnOnce(&[u8]) -> Result<[u8; SIG_LEN]>,
    {
        if body.delegations.is_empty()
            && body.founder_puddle_pubkey.to_ascii_lowercase()
                != body.signing_puddle_pubkey.to_ascii_lowercase()
        {
            bail!(
                "catalog entry has no delegation chain but signing_puddle_pubkey ({}) ≠ founder_puddle_pubkey ({}) — a rekeyed founder must carry the ADR-019 delegation chain",
                body.signing_puddle_pubkey,
                body.founder_puddle_pubkey
            );
        }
        let msg = canonical_bytes(&body)?;
        let sig = sign(&msg)?;
        let entry = CatalogEntry {
            body,
            signature: hex::encode(sig),
        };
        entry
            .verify(DELEGATION_MAX_LEN)
            .context("freshly-built catalog entry failed self-verification")?;
        Ok(entry)
    }

    /// The artifact_id this entry is anchored to: `sha256(founder_puddle_pubkey ‖ name)`.
    pub fn artifact_id(&self) -> Result<[u8; ID_LEN]> {
        let founder = parse_hex32(&self.body.founder_puddle_pubkey, "founder_puddle_pubkey")?;
        Ok(artifact_id_for(&founder, &self.body.name))
    }

    /// Hex form of [`Self::artifact_id`].
    pub fn artifact_id_hex(&self) -> Result<String> {
        Ok(hex::encode(self.artifact_id()?))
    }

    /// Full anchor-rooted verification: (1) the entry's Ed25519 signature is
    /// valid under `signing_puddle_pubkey` over `canonical_bytes(body)`; (2) the
    /// delegation chain — if empty, `signing == founder`; if non-empty, it's
    /// ≤ `max_chain_len`, each link's own signature verifies, each link is *for
    /// this artifact* (`project_id == artifact_id`), the links bridge
    /// `founder → … → signing`, and `delegated_at` strictly increases.
    ///
    /// Does NOT itself check that `artifact_id` equals any externally-expected id
    /// — the aggregator keys on [`Self::artifact_id`] and rejects collisions.
    pub fn verify(&self, max_chain_len: usize) -> Result<()> {
        // (1) entry signature.
        let signer = parse_hex32(&self.body.signing_puddle_pubkey, "signing_puddle_pubkey")?;
        let vk = VerifyingKey::from_bytes(&signer).map_err(|e| anyhow!("invalid signing pubkey: {e}"))?;
        let sig_bytes = hex::decode(&self.signature).context("catalog entry signature hex")?;
        if sig_bytes.len() != SIG_LEN {
            bail!("catalog entry signature is {} bytes, expected {}", sig_bytes.len(), SIG_LEN);
        }
        let mut sig_arr = [0u8; SIG_LEN];
        sig_arr.copy_from_slice(&sig_bytes);
        let sig = Signature::from_bytes(&sig_arr);
        let msg = canonical_bytes(&self.body)?;
        vk.verify(&msg, &sig)
            .map_err(|e| anyhow!("catalog entry signature verification failed: {e}"))?;

        // (2) delegation chain.
        let founder = parse_hex32(&self.body.founder_puddle_pubkey, "founder_puddle_pubkey")?;
        let artifact_id = artifact_id_for(&founder, &self.body.name);
        if self.body.delegations.is_empty() {
            if signer != founder {
                bail!(
                    "catalog entry has no delegation chain but the signing pubkey ≠ the founder pubkey it's anchored to"
                );
            }
            return Ok(());
        }
        if self.body.delegations.len() > max_chain_len {
            bail!(
                "delegation chain is {} hops, over the {}-hop cap",
                self.body.delegations.len(),
                max_chain_len
            );
        }
        let mut prev_new = founder;
        let mut prev_at: Option<i64> = None;
        for (i, env) in self.body.delegations.iter().enumerate() {
            let raw = env
                .verify_signature()
                .with_context(|| format!("delegation #{i} signature"))?;
            if raw.project_id != artifact_id {
                bail!("delegation #{i} is for a different artifact (project_id mismatch)");
            }
            if raw.old_founder_pubkey != prev_new {
                bail!("delegation chain broken at #{i}: doesn't continue from the previous link");
            }
            if let Some(p) = prev_at {
                if env.delegated_at <= p {
                    bail!("delegation #{i} is not strictly later than #{}", i - 1);
                }
            }
            prev_at = Some(env.delegated_at);
            prev_new = raw.new_founder_pubkey;
        }
        if prev_new != signer {
            bail!("delegation chain doesn't terminate at the entry's signing pubkey");
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn pub_hex(k: &SigningKey) -> String {
        hex::encode(k.verifying_key().to_bytes())
    }

    fn body_for(founder: &SigningKey, signer: &SigningKey, name: &str) -> CatalogEntryBody {
        let mut versions = BTreeMap::new();
        versions.insert(
            "1.0.0".to_string(),
            ArtifactVersion {
                platforms: vec![Platform {
                    os: "linux".to_string(),
                    arch: "amd64".to_string(),
                    blob_sha: "a".repeat(64),
                    size: 12345,
                }],
            },
        );
        let mut tags = BTreeMap::new();
        tags.insert("latest".to_string(), "1.0.0".to_string());
        CatalogEntryBody {
            schema_version: 1,
            name: name.to_string(),
            founder_puddle_pubkey: pub_hex(founder),
            signing_puddle_pubkey: pub_hex(signer),
            versions,
            tags,
            recipients: vec!["*".to_string()],
            target: "*".to_string(),
            issued_at: 1,
            delegations: vec![],
        }
    }

    #[test]
    fn issue_and_verify_roundtrip() {
        let k = fresh_key();
        let entry = CatalogEntry::issue(&k, body_for(&k, &k, "vigo-agent")).unwrap();
        entry.verify(DELEGATION_MAX_LEN).unwrap();
        let want = {
            let mut h = Sha256::new();
            h.update(k.verifying_key().to_bytes());
            h.update(b"vigo-agent");
            hex::encode(h.finalize())
        };
        assert_eq!(entry.artifact_id_hex().unwrap(), want);
    }

    #[test]
    fn issue_rejects_signer_mismatch() {
        let k = fresh_key();
        let other = fresh_key();
        let mut body = body_for(&k, &k, "x");
        body.signing_puddle_pubkey = pub_hex(&other);
        assert!(CatalogEntry::issue(&k, body).is_err());
    }

    #[test]
    fn issue_rejects_no_chain_founder_mismatch() {
        let founder = fresh_key();
        let signer = fresh_key();
        // signer ≠ founder but no delegation chain — must be refused at issue time.
        let body = body_for(&founder, &signer, "x");
        assert!(CatalogEntry::issue(&signer, body).is_err());
    }

    #[test]
    fn tampered_body_rejected() {
        let k = fresh_key();
        let base = CatalogEntry::issue(&k, body_for(&k, &k, "x")).unwrap();

        let mut t = base.clone();
        t.body.name = "y".to_string();
        assert!(t.verify(DELEGATION_MAX_LEN).is_err());

        let mut t = base.clone();
        t.body.issued_at = 999;
        assert!(t.verify(DELEGATION_MAX_LEN).is_err());

        let mut t = base.clone();
        t.body
            .versions
            .get_mut("1.0.0")
            .unwrap()
            .platforms[0]
            .blob_sha = "b".repeat(64);
        assert!(t.verify(DELEGATION_MAX_LEN).is_err());

        let mut t = base.clone();
        t.body.recipients = vec!["deadbeef".repeat(8)];
        assert!(t.verify(DELEGATION_MAX_LEN).is_err());
    }

    #[test]
    fn issue_with_delegated_signer_matches_in_process() {
        let k = fresh_key();
        let direct = CatalogEntry::issue(&k, body_for(&k, &k, "x")).unwrap();
        let delegated = CatalogEntry::issue_with(body_for(&k, &k, "x"), |m| Ok(k.sign(m).to_bytes())).unwrap();
        assert_eq!(direct, delegated);
    }

    #[test]
    fn valid_one_link_delegation_chain() {
        let k1 = fresh_key(); // genesis founder
        let k2 = fresh_key(); // rekeyed-to founder
        let artifact_id = {
            let mut h = Sha256::new();
            h.update(k1.verifying_key().to_bytes());
            h.update(b"x");
            let d = h.finalize();
            let mut a = [0u8; 32];
            a.copy_from_slice(&d);
            a
        };
        let mut body = body_for(&k1, &k2, "x");
        body.delegations = vec![DelegationEnvelope::build(
            &k1,
            &artifact_id,
            &k2.verifying_key().to_bytes(),
            100,
        )];
        let entry = CatalogEntry::issue(&k2, body).unwrap();
        entry.verify(DELEGATION_MAX_LEN).unwrap();
    }

    #[test]
    fn delegation_chain_rejects_wrong_artifact() {
        let k1 = fresh_key();
        let k2 = fresh_key();
        // A delegation whose project_id is for a *different* artifact.
        let wrong_id = artifact_id_for(&k1.verifying_key().to_bytes(), "OTHER");
        let mut body = body_for(&k1, &k2, "x");
        body.delegations = vec![DelegationEnvelope::build(
            &k1,
            &wrong_id,
            &k2.verifying_key().to_bytes(),
            100,
        )];
        let entry = CatalogEntry::issue_with(body, |m| Ok(k2.sign(m).to_bytes())).unwrap_err();
        let _ = entry; // issue self-verifies, so this fails at issue time.
    }

    #[test]
    fn delegation_chain_rejects_non_monotonic() {
        let k1 = fresh_key();
        let k2 = fresh_key();
        let k3 = fresh_key();
        let id = artifact_id_for(&k1.verifying_key().to_bytes(), "x");
        let mut body = body_for(&k1, &k3, "x");
        body.delegations = vec![
            DelegationEnvelope::build(&k1, &id, &k2.verifying_key().to_bytes(), 200),
            DelegationEnvelope::build(&k2, &id, &k3.verifying_key().to_bytes(), 100), // earlier than the first — invalid
        ];
        assert!(CatalogEntry::issue_with(body, |m| Ok(k3.sign(m).to_bytes())).is_err());
    }

    #[test]
    fn delegation_chain_rejects_broken_link() {
        let k1 = fresh_key();
        let k2 = fresh_key();
        let k3 = fresh_key();
        let k4 = fresh_key();
        let id = artifact_id_for(&k1.verifying_key().to_bytes(), "x");
        let mut body = body_for(&k1, &k3, "x");
        // Second link is signed by k4, not k2 — doesn't continue the chain.
        body.delegations = vec![
            DelegationEnvelope::build(&k1, &id, &k2.verifying_key().to_bytes(), 100),
            DelegationEnvelope::build(&k4, &id, &k3.verifying_key().to_bytes(), 200),
        ];
        assert!(CatalogEntry::issue_with(body, |m| Ok(k3.sign(m).to_bytes())).is_err());
    }

    #[test]
    fn delegation_chain_over_cap_rejected() {
        // Build a chain longer than the cap and verify directly with a small cap.
        let k1 = fresh_key();
        let k2 = fresh_key();
        let id = artifact_id_for(&k1.verifying_key().to_bytes(), "x");
        let mut body = body_for(&k1, &k2, "x");
        body.delegations = vec![DelegationEnvelope::build(
            &k1,
            &id,
            &k2.verifying_key().to_bytes(),
            100,
        )];
        let entry = CatalogEntry::issue(&k2, body).unwrap();
        assert!(entry.verify(0).is_err()); // 1-hop chain, 0-hop cap → rejected
    }

    #[test]
    fn validate_artifact_name_rules() {
        assert!(validate_artifact_name("vigo-agent").is_ok());
        assert!(validate_artifact_name("app-v2.tar.gz").is_ok());
        assert!(validate_artifact_name("a").is_ok());
        assert!(validate_artifact_name("").is_err());
        assert!(validate_artifact_name(".hidden").is_err());
        assert!(validate_artifact_name("bad name").is_err());
        assert!(validate_artifact_name("path/sep").is_err());
        assert!(validate_artifact_name(&"a".repeat(65)).is_err());
        assert!(validate_artifact_name(&"a".repeat(64)).is_err()); // 64 lowercase hex
        assert!(validate_artifact_name(&format!("g{}", "a".repeat(63))).is_ok()); // 64 chars but not all hex
    }
}
