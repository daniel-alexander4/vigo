// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Delegation envelopes (ADR-019) — the bridge that anchors a token
//! chain across a founder-puddle rekey.
//!
//! When a puddle rotates its Ed25519 keypair (per ADR-018), every project
//! it founded keeps its `resource_id = sha256(genesis_pubkey ‖ name)` —
//! the hash inputs are frozen forever. Tokens minted post-rekey are
//! signed by the *new* pubkey, which no longer matches the genesis
//! pubkey baked into `resource_id`. The chain would dead-end at the
//! rotation.
//!
//! A DELEGATION envelope, signed by the OLD founder pubkey during the
//! rekey ceremony, attests "I, the genesis pubkey, hereby delegate
//! founder authority for this resource to <new pubkey>." Future tokens
//! chain through the new pubkey; verification walks the delegation
//! chain back to genesis to confirm authority. Cap: 5 links per
//! [`DELEGATION_MAX_LEN`]; past that the only path forward is
//! re-founding (mints a new resource_id).
//!
//! ### Why this lives in anchor
//!
//! Multiple swarm content subsystems sign things keyed to a founder
//! puddle pubkey — gitback's capability tokens (ADR-015), curator's
//! catalog entries (ADR-024), the ADR-022 friendly-name claims. All
//! need the same rekey-bridge mechanism, all share the same wire
//! shape. Keeping the envelope and its canonical-bytes function in
//! anchor is the single source of truth.
//!
//! ### Wire-compat note (byte-preserved domain string)
//!
//! [`DELEGATION_DOMAIN`] = `b"gitback-delegate-v2\0"` is **byte-preserved**
//! across this module's `gitback → anchor` relocation. Every delegation
//! signed in the fleet today verifies against this exact value, and a
//! rename to `anchor-delegate-v1` would invalidate every existing
//! rekey-bridge across every project on every envoy. The `gitback-`
//! name is legacy origin, not current scope.

use anyhow::{anyhow, bail, Context, Result};
use ed25519_dalek::{Signature, Signer, SigningKey, Verifier, VerifyingKey};
use serde::{Deserialize, Serialize};

use crate::swarm::anchor::tokens::{ID_LEN, SIG_LEN};

/// Domain prefix for canonical bytes signed in a DELEGATION envelope.
///
/// Distinct from every other signed-envelope domain in the swarm so a
/// delegation signature can never be replayed as a token, revoke, or
/// leave. The byte string is **byte-preserved** across the `gitback →
/// anchor` relocation (see module docs); renaming would invalidate
/// every delegation signed in the fleet today.
pub const DELEGATION_DOMAIN: &[u8] = b"gitback-delegate-v2\0";

/// ADR-019 chain hygiene: warn the operator at this length. Status
/// surfaces a yellow flag per affected resource saying "consider
/// re-founding for hygiene." Soft signal, not enforced.
pub const DELEGATION_WARN_LEN: usize = 3;

/// ADR-019 hard cap. The pre-rotation hook in `puddle rekey-pair`
/// aborts the rekey when any resource is at this length, and chain
/// verifiers (tokens / catalog-entries / name-claims) reject any
/// presented chain that exceeds it. Past the cap the operator's only
/// path forward is to re-found the resource (mints a new resource_id)
/// or `--force-orphan-projects` on `rekey-pair`.
pub const DELEGATION_MAX_LEN: usize = 5;

/// Canonical bytes signed in a DELEGATION envelope. Hand-rolled
/// deterministic encoding — NEVER use serde here; JSON whitespace and
/// field-order ambiguity would silently produce two different signed
/// messages for what looks like the same envelope.
///
/// Layout:
/// 1. [`DELEGATION_DOMAIN`] (20B)
/// 2. `project_id` (32B) — frozen anchor; the resource's hash-input
///    pubkey is baked in here so a delegation under resource A cannot
///    be replayed as a delegation under resource B.
/// 3. `old_founder_pubkey` (32B) — the signer.
///    For the first delegation in a resource's chain, this is the
///    genesis founder pubkey. For subsequent delegations, this is the
///    previous link's `new_founder_pubkey` (i.e. the most-recent
///    rekey's outgoing key).
/// 4. `new_founder_pubkey` (32B) — the delegated-to key (the rekey
///    target). Future signatures chain through this pubkey until
///    the next rekey appends another delegation.
/// 5. `delegated_at` (8B BE i64).
///
/// `signer_puddle_pubkey` is intentionally absent — `old_founder_pubkey`
/// IS the signer, so re-including it would be redundant. The verifier
/// asserts the binding during signature check.
pub fn canonical_delegation_bytes(
    project_id: &[u8; ID_LEN],
    old_founder_pubkey: &[u8; ID_LEN],
    new_founder_pubkey: &[u8; ID_LEN],
    delegated_at: i64,
) -> Vec<u8> {
    let mut out = Vec::with_capacity(DELEGATION_DOMAIN.len() + 32 + 32 + 32 + 8);
    out.extend_from_slice(DELEGATION_DOMAIN);
    out.extend_from_slice(project_id);
    out.extend_from_slice(old_founder_pubkey);
    out.extend_from_slice(new_founder_pubkey);
    out.extend_from_slice(&delegated_at.to_be_bytes());
    out
}

/// A signed delegation envelope (ADR-019). JSON-serialized for
/// persistence and gossip; hex-string fields keep the file
/// human-readable. Raw byte fields convert via [`Self::raw`] for
/// signature verification or chain-walk integrity checks.
///
/// Threat-model note: delegations are *informational hints* when
/// received over the gossip rail — receivers persist them to bridge
/// signature chains but never treat the bare delegation as
/// authority. Authority comes from a chain that walks all the way
/// back to the genesis pubkey baked into the resource_id; the
/// delegations are the optional bridges that make a post-rekey chain
/// valid, not standalone grants.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct DelegationEnvelope {
    /// Hex-encoded resource anchor (e.g. gitback `project_id`, curator
    /// `artifact_id`, ADR-022 name-claim id). Frozen across all rekeys.
    pub project_id: String,
    /// Hex-encoded puddle pubkey that signed THIS delegation. For the
    /// first delegation in a resource's chain, equals the genesis
    /// founder pubkey baked into the resource_id. For subsequent
    /// delegations, equals the previous link's `new_founder_pubkey`.
    pub old_founder_pubkey: String,
    /// Hex-encoded puddle pubkey that this delegation grants founder
    /// authority to. Future signatures / further delegations chain
    /// through this pubkey until the next rekey.
    pub new_founder_pubkey: String,
    /// Unix seconds when the delegation was signed.
    pub delegated_at: i64,
    /// Hex-encoded 64-byte Ed25519 signature.
    pub signature: String,
}

/// Decoded raw form of a [`DelegationEnvelope`] — bytes ready for
/// signature verification or chain-walk integrity checks.
pub struct RawDelegation {
    pub project_id: [u8; ID_LEN],
    pub old_founder_pubkey: [u8; ID_LEN],
    pub new_founder_pubkey: [u8; ID_LEN],
    pub signature: [u8; SIG_LEN],
}

impl DelegationEnvelope {
    /// Build + sign a delegation under the OLD founder key. Caller
    /// supplies the OLD signing key (during a rekey ceremony, this
    /// key is still unwrapped in process memory). The envelope's
    /// `old_founder_pubkey` is derived from the public half so a
    /// key/pubkey mismatch fails loudly.
    pub fn build(
        old_signing_key: &SigningKey,
        project_id: &[u8; ID_LEN],
        new_founder_pubkey: &[u8; ID_LEN],
        delegated_at: i64,
    ) -> Self {
        let old_pub: [u8; ID_LEN] = old_signing_key.verifying_key().to_bytes();
        let msg = canonical_delegation_bytes(
            project_id,
            &old_pub,
            new_founder_pubkey,
            delegated_at,
        );
        let sig = old_signing_key.sign(&msg);
        Self {
            project_id: hex::encode(project_id),
            old_founder_pubkey: hex::encode(old_pub),
            new_founder_pubkey: hex::encode(new_founder_pubkey),
            delegated_at,
            signature: hex::encode(sig.to_bytes()),
        }
    }

    /// Build with a delegated signer (e.g. the puddle session helper,
    /// which holds the unwrapped signing key in a separate process).
    /// Returns an envelope that has been self-verified — bad
    /// caller-supplied signatures fail loudly here rather than at
    /// chain-walk time.
    pub fn build_with<F>(
        project_id: &[u8; ID_LEN],
        old_founder_pubkey: &[u8; ID_LEN],
        new_founder_pubkey: &[u8; ID_LEN],
        delegated_at: i64,
        sign: F,
    ) -> Result<Self>
    where
        F: FnOnce(&[u8]) -> Result<[u8; SIG_LEN]>,
    {
        let msg = canonical_delegation_bytes(
            project_id,
            old_founder_pubkey,
            new_founder_pubkey,
            delegated_at,
        );
        let sig = sign(&msg)?;
        let env = Self {
            project_id: hex::encode(project_id),
            old_founder_pubkey: hex::encode(old_founder_pubkey),
            new_founder_pubkey: hex::encode(new_founder_pubkey),
            delegated_at,
            signature: hex::encode(sig),
        };
        env.verify_signature()
            .context("freshly-built delegation envelope failed self-verification")?;
        Ok(env)
    }

    /// Decode all hex fields into raw byte arrays.
    pub fn raw(&self) -> Result<RawDelegation> {
        let project_id = parse_id_hex(&self.project_id).context("project_id hex")?;
        let old_founder_pubkey = parse_id_hex(&self.old_founder_pubkey)
            .context("old_founder_pubkey hex")?;
        let new_founder_pubkey = parse_id_hex(&self.new_founder_pubkey)
            .context("new_founder_pubkey hex")?;
        let sig_bytes = hex::decode(&self.signature).context("signature hex")?;
        if sig_bytes.len() != SIG_LEN {
            bail!("signature is {} bytes, expected {}", sig_bytes.len(), SIG_LEN);
        }
        let mut signature = [0u8; SIG_LEN];
        signature.copy_from_slice(&sig_bytes);
        Ok(RawDelegation {
            project_id,
            old_founder_pubkey,
            new_founder_pubkey,
            signature,
        })
    }

    /// Verify the Ed25519 signature on this envelope against
    /// `old_founder_pubkey` (the embedded signer). Does NOT verify
    /// chain integrity — that's the consumer's job
    /// ([`super::tokens::verify_chain`] for gitback,
    /// `CatalogEntry::verify` for curator).
    pub fn verify_signature(&self) -> Result<RawDelegation> {
        let raw = self.raw()?;
        let vk = VerifyingKey::from_bytes(&raw.old_founder_pubkey)
            .map_err(|e| anyhow!("invalid old founder pubkey: {}", e))?;
        let sig = Signature::from_bytes(&raw.signature);
        let msg = canonical_delegation_bytes(
            &raw.project_id,
            &raw.old_founder_pubkey,
            &raw.new_founder_pubkey,
            self.delegated_at,
        );
        vk.verify(&msg, &sig)
            .map_err(|e| anyhow!("signature verification failed: {}", e))?;
        Ok(raw)
    }
}

/// Strict-form ID parser: exactly `ID_LEN * 2` lowercase hex chars,
/// inverse of `hex::encode(id)`. Local to this module so anchor doesn't
/// depend on any other subsystem's project-state utility — the same
/// shape would otherwise live in three or four places.
fn parse_id_hex(s: &str) -> Result<[u8; ID_LEN]> {
    if s.len() != ID_LEN * 2 {
        bail!("expected {}-char hex id, got {} chars", ID_LEN * 2, s.len());
    }
    if s.chars().any(|c| c.is_ascii_uppercase()) {
        bail!("id must be lowercase hex");
    }
    let bytes = hex::decode(s).map_err(|e| anyhow!("invalid hex: {}", e))?;
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&bytes);
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use rand::rngs::OsRng;

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn pub_bytes(k: &SigningKey) -> [u8; ID_LEN] {
        k.verifying_key().to_bytes()
    }

    #[test]
    fn canonical_bytes_are_deterministic() {
        let pid = [0x42u8; ID_LEN];
        let old = [0x11u8; ID_LEN];
        let new = [0x22u8; ID_LEN];
        let a = canonical_delegation_bytes(&pid, &old, &new, 1700000000);
        let b = canonical_delegation_bytes(&pid, &old, &new, 1700000000);
        assert_eq!(a, b);
    }

    #[test]
    fn canonical_bytes_change_when_inputs_change() {
        let pid = [0x42u8; ID_LEN];
        let old = [0x11u8; ID_LEN];
        let new1 = [0x22u8; ID_LEN];
        let new2 = [0x33u8; ID_LEN];
        let a = canonical_delegation_bytes(&pid, &old, &new1, 1700000000);
        let b = canonical_delegation_bytes(&pid, &old, &new2, 1700000000);
        assert_ne!(a, b);
        let c = canonical_delegation_bytes(&pid, &old, &new1, 1700000001);
        assert_ne!(a, c);
    }

    #[test]
    fn domain_prefix_is_in_canonical_bytes() {
        let pid = [0u8; ID_LEN];
        let old = [0u8; ID_LEN];
        let new = [0u8; ID_LEN];
        let bytes = canonical_delegation_bytes(&pid, &old, &new, 0);
        assert!(bytes.starts_with(DELEGATION_DOMAIN));
    }

    #[test]
    fn domain_prefix_is_byte_preserved() {
        // Guardrail against accidental wire breaks. The byte string here
        // must equal what gitback/envelopes.rs signed under before the
        // anchor extraction. If you're changing this, you're invalidating
        // every delegation in the fleet — make sure that's intentional.
        assert_eq!(DELEGATION_DOMAIN, b"gitback-delegate-v2\0");
    }

    #[test]
    fn build_and_self_verify() {
        let pid = [0x42u8; ID_LEN];
        let old_key = fresh_key();
        let new_pub = pub_bytes(&fresh_key());
        let env = DelegationEnvelope::build(&old_key, &pid, &new_pub, 1700000000);
        let raw = env.verify_signature().unwrap();
        assert_eq!(raw.project_id, pid);
        assert_eq!(raw.old_founder_pubkey, pub_bytes(&old_key));
        assert_eq!(raw.new_founder_pubkey, new_pub);
    }

    #[test]
    fn tampered_signature_fails() {
        let pid = [0x42u8; ID_LEN];
        let old_key = fresh_key();
        let new_pub = pub_bytes(&fresh_key());
        let mut env = DelegationEnvelope::build(&old_key, &pid, &new_pub, 1700000000);
        // Flip one nibble of the sig.
        let first = env.signature.chars().next().unwrap();
        let flipped = if first == '0' { '1' } else { '0' };
        env.signature = format!("{flipped}{}", &env.signature[1..]);
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn tampered_new_pubkey_fails() {
        let pid = [0x42u8; ID_LEN];
        let old_key = fresh_key();
        let new_pub = pub_bytes(&fresh_key());
        let mut env = DelegationEnvelope::build(&old_key, &pid, &new_pub, 1700000000);
        // Swap to a different new pubkey hex — signature was over the original.
        env.new_founder_pubkey = hex::encode([0xFFu8; ID_LEN]);
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn build_with_self_verifies_and_fails_on_bad_sig() {
        let pid = [0x42u8; ID_LEN];
        let old_pub = pub_bytes(&fresh_key());
        let new_pub = pub_bytes(&fresh_key());
        // A signer that returns junk bytes should fail self-verification.
        let bad = DelegationEnvelope::build_with(&pid, &old_pub, &new_pub, 1, |_| {
            Ok([0u8; SIG_LEN])
        });
        assert!(bad.is_err());
    }

    #[test]
    fn parse_id_hex_rejects_wrong_length_and_uppercase() {
        assert!(parse_id_hex("").is_err());
        assert!(parse_id_hex(&"a".repeat(63)).is_err());
        assert!(parse_id_hex(&"a".repeat(65)).is_err());
        assert!(parse_id_hex(&"A".repeat(64)).is_err());
        assert!(parse_id_hex(&"a".repeat(64)).is_ok());
    }
}
