// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Revocation + leave + delegation envelopes for ADR-015 / ADR-019
//! unified-substrate gitmirror.
//!
//! A REVOKE envelope retracts a capability token; a LEAVE envelope
//! announces a puddle stepping out of a project; a DELEGATION envelope
//! (ADR-019) is the bridge a founder signs at puddle rekey time so
//! that admin tokens minted under the new puddle keypair still chain
//! back to the founder pubkey baked into `project_id`. All three are
//! signed Ed25519 blobs that gossip across the swarm and persist on
//! every envoy that holds the project — except DELEGATION envelopes
//! gossip as informational hints only, never as authority (see
//! [`DelegationEnvelope`] for the threat-model rationale).
//!
//! Design mirrors [`super::tokens`] (canonical hand-rolled encoding,
//! distinct domain prefix per envelope type) and
//! [`crate::swarm::cryptsync::leaves`] (90-day reap, idempotent
//! upsert, persistence on the daemon-only canonical path).
//!
//! ### Trust model
//!
//! - **REVOKE** must be signed by an authorized admin in the project's
//!   token tree. The envelope carries `signer_puddle_pubkey`; the
//!   verifier checks the signature against that pubkey, then separately
//!   asks "is this puddle a current admin in the project?" by looking
//!   up an admin token whose subject == signer in the local
//!   `tokens.json`. (The lookup is the caller's responsibility; this
//!   module only verifies signatures and structure.)
//! - **LEAVE** is self-leave only: the signer must be the leaving
//!   fleet itself. The envelope carries `leaving_puddle_pubkey`; the
//!   signature is verified against that same pubkey. Cross-puddle
//!   eviction is cryptographically impossible — admin removal is a
//!   REVOKE, not a LEAVE.
//!
//! ### Persistence
//!
//! `<gitmirror_base>/envelopes/<project_id_hex>/{revokes,leaves}/<id>.json`
//! - `revokes/` keyed by `token_id_hex` (stable per token; one
//!   revocation per token).
//! - `leaves/` keyed by `leaving_puddle_pubkey_hex`.
//!
//! Files are root-owned 0600. Idempotent upsert: re-arrival of the
//! same envelope is a no-op; arrival of an envelope with a strictly
//! later timestamp replaces the existing one.
//!
//! ### Reap
//!
//! Records older than [`DEFAULT_RETENTION_SECS`] (90 days) are reaped
//! by the helper-daemon reactor (step 4 of ADR-015). Tokens revoked
//! more than 90 days ago are presumed to also be expired by their own
//! `expires_at`; if not, the token holder must rotate.

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use anyhow::{anyhow, bail, Context, Result};
use ed25519_dalek::{Signature, Signer, SigningKey, Verifier, VerifyingKey};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use super::project_state::{hex_id, parse_hex_id};
use super::tokens::{ID_LEN, SIG_LEN};

/// Domain prefix for canonical bytes signed in a REVOKE envelope.
/// Distinct from [`super::tokens::TOKEN_DOMAIN`] and [`LEAVE_DOMAIN`]
/// so a signature over one shape can never be mistaken for another.
pub const REVOKE_DOMAIN: &[u8] = b"vigo-gitmirror-revoke-v1\0";

/// Domain prefix for canonical bytes signed in a LEAVE envelope.
pub const LEAVE_DOMAIN: &[u8] = b"vigo-gitmirror-leave-v1\0";

/// Domain prefix for canonical bytes signed in a DELEGATION envelope
/// (ADR-019). Distinct from every other domain so a delegation
/// signature can never be replayed as a different envelope type.
pub const DELEGATION_DOMAIN: &[u8] = b"vigo-gitmirror-delegate-v1\0";

/// Compile-time fallback reap window — 90 days, matching cryptsync's
/// tombstone + leave retention. Read sites should prefer
/// [`effective_retention_secs`] which consults the server-distributed
/// `swarm.gitmirror.envelope_retention` setting.
pub const DEFAULT_RETENTION_SECS: i64 = 90 * 24 * 60 * 60;

/// Returns the operator-configured envelope retention from
/// `server.yaml swarm.gitmirror.envelope_retention`; falls back to
/// [`DEFAULT_RETENTION_SECS`] when unset.
pub fn effective_retention_secs() -> i64 {
    crate::swarm::runtime_limits::gitmirror_envelope_retention_secs()
}

/// ADR-019 chain hygiene: warn the operator at this length. Status
/// surfaces a yellow flag per affected project saying "consider
/// re-founding for hygiene." Soft signal, not enforced.
pub const DELEGATION_WARN_LEN: usize = 3;

/// ADR-019 hard cap. The pre-rotation hook in `cmd_rekey_pair` aborts
/// the rekey when a project is at this length, and `verify_chain`
/// rejects any presented chain that exceeds it. Past the cap the
/// operator's only path forward is `gitmirror project re-found
/// <handle>` (which mints a new project_id) or
/// `--force-orphan-projects` on `rekey-pair`.
pub const DELEGATION_MAX_LEN: usize = 5;

/// Length of an aggregate digest (used by gossip catch-up). 16 bytes
/// of sha256 — same width cryptsync uses.
pub const DIGEST_LEN: usize = 16;

// ---------- canonical signed bytes ----------

/// Canonical bytes signed when issuing a REVOKE. Hand-rolled,
/// length-prefixed where strings appear, so two envelopes with the
/// same field tuple always produce byte-identical input to Ed25519.
///
/// Layout:
/// 1. `REVOKE_DOMAIN` (24B)
/// 2. `project_id` (32B)
/// 3. `token_id` (32B)
/// 4. `revoked_at` (8B BE i64)
/// 5. `reason_len` (4B BE u32) + `reason` (varlen UTF-8)
/// 6. `signer_puddle_pubkey` (32B)
pub fn canonical_revoke_bytes(
    project_id: &[u8; ID_LEN],
    token_id: &[u8; ID_LEN],
    revoked_at: i64,
    reason: &str,
    signer_puddle_pubkey: &[u8; ID_LEN],
) -> Vec<u8> {
    let mut out = Vec::with_capacity(REVOKE_DOMAIN.len() + 32 + 32 + 8 + 4 + reason.len() + 32);
    out.extend_from_slice(REVOKE_DOMAIN);
    out.extend_from_slice(project_id);
    out.extend_from_slice(token_id);
    out.extend_from_slice(&revoked_at.to_be_bytes());
    let reason_bytes = reason.as_bytes();
    out.extend_from_slice(&(reason_bytes.len() as u32).to_be_bytes());
    out.extend_from_slice(reason_bytes);
    out.extend_from_slice(signer_puddle_pubkey);
    out
}

/// Canonical bytes signed when issuing a LEAVE. Layout:
/// 1. `LEAVE_DOMAIN` (23B)
/// 2. `project_id` (32B)
/// 3. `leaving_puddle_pubkey` (32B)
/// 4. `leave_time` (8B BE i64)
///
/// `signer_puddle_pubkey` is intentionally absent — self-leave invariant
/// pins it to `leaving_puddle_pubkey`, so re-including it would just
/// be redundant. Verifier asserts the pin during signature check.
pub fn canonical_leave_bytes(
    project_id: &[u8; ID_LEN],
    leaving_puddle_pubkey: &[u8; ID_LEN],
    leave_time: i64,
) -> Vec<u8> {
    let mut out = Vec::with_capacity(LEAVE_DOMAIN.len() + 32 + 32 + 8);
    out.extend_from_slice(LEAVE_DOMAIN);
    out.extend_from_slice(project_id);
    out.extend_from_slice(leaving_puddle_pubkey);
    out.extend_from_slice(&leave_time.to_be_bytes());
    out
}

/// Canonical bytes signed when issuing a DELEGATION envelope
/// (ADR-019). Layout:
/// 1. `DELEGATION_DOMAIN` (26B)
/// 2. `project_id` (32B) — anchors to the genesis founder pubkey via
///    the `sha256` baked into the URL; never changes across rekeys.
/// 3. `old_founder_pubkey` (32B) — the *signer* of this envelope.
///    For the first delegation in a project's chain, this is the
///    genesis founder pubkey. For subsequent delegations, this is the
///    previous link's `new_founder_pubkey` (i.e. the most-recent
///    rekey's outgoing key).
/// 4. `new_founder_pubkey` (32B) — the delegated-to key (the rekey
///    target). Future admin tokens chain through this pubkey until
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

// ---------- envelope shapes ----------

/// A signed revocation envelope. JSON-serialized for persistence and
/// gossip; hex-string fields keep the file human-readable. Raw byte
/// fields convert via [`Self::raw`] for verification.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct RevokeEnvelope {
    /// Hex-encoded `sha256(founder_puddle_pubkey)` — the project ID
    /// baked into the URL.
    pub project_id: String,
    /// Hex-encoded ID of the token being revoked.
    pub token_id: String,
    /// Unix seconds when the envelope was signed.
    pub revoked_at: i64,
    /// Free-form audit reason (length-prefixed in canonical bytes so
    /// it can't bleed into the next field).
    #[serde(default)]
    pub reason: String,
    /// Hex-encoded puddle pubkey that signed this envelope. Must be an
    /// authorized admin in the project's token tree (caller verifies
    /// after signature passes — see module docs).
    pub signer_puddle_pubkey: String,
    /// Hex-encoded 64-byte Ed25519 signature.
    pub signature: String,
}

/// Decoded raw form of a [`RevokeEnvelope`] — bytes ready for
/// signature verification.
pub struct RawRevoke {
    pub project_id: [u8; ID_LEN],
    pub token_id: [u8; ID_LEN],
    pub signer_puddle_pubkey: [u8; ID_LEN],
    pub signature: [u8; SIG_LEN],
}

impl RevokeEnvelope {
    /// Build + sign a revocation. Caller supplies the admin's signing
    /// key; the envelope's `signer_puddle_pubkey` is derived from the
    /// public half so a key/pubkey mismatch fails loudly.
    pub fn build(
        signing_key: &SigningKey,
        project_id: &[u8; ID_LEN],
        token_id: &[u8; ID_LEN],
        revoked_at: i64,
        reason: &str,
    ) -> Self {
        let signer_pub: [u8; ID_LEN] = signing_key.verifying_key().to_bytes();
        let msg = canonical_revoke_bytes(project_id, token_id, revoked_at, reason, &signer_pub);
        let sig = signing_key.sign(&msg);
        Self {
            project_id: hex_id(project_id),
            token_id: hex_id(token_id),
            revoked_at,
            reason: reason.to_string(),
            signer_puddle_pubkey: hex_id(&signer_pub),
            signature: hex::encode(sig.to_bytes()),
        }
    }

    /// Build a REVOKE where signing is delegated to a closure (e.g.
    /// the puddle session helper, which holds the unwrapped
    /// signing key in a separate process). Returns an envelope that
    /// has been self-verified — bad caller-supplied signatures fail
    /// loudly here rather than at gossip time.
    pub fn build_with<F>(
        project_id: &[u8; ID_LEN],
        token_id: &[u8; ID_LEN],
        revoked_at: i64,
        reason: &str,
        signer_puddle_pubkey: &[u8; ID_LEN],
        sign: F,
    ) -> Result<Self>
    where
        F: FnOnce(&[u8]) -> Result<[u8; SIG_LEN]>,
    {
        let msg = canonical_revoke_bytes(
            project_id,
            token_id,
            revoked_at,
            reason,
            signer_puddle_pubkey,
        );
        let sig = sign(&msg)?;
        let env = Self {
            project_id: hex_id(project_id),
            token_id: hex_id(token_id),
            revoked_at,
            reason: reason.to_string(),
            signer_puddle_pubkey: hex_id(signer_puddle_pubkey),
            signature: hex::encode(sig),
        };
        env.verify_signature()
            .context("freshly-built revoke envelope failed self-verification")?;
        Ok(env)
    }

    /// Decode all hex fields into raw byte arrays for verification.
    pub fn raw(&self) -> Result<RawRevoke> {
        let project_id = parse_hex_id(&self.project_id).context("project_id hex")?;
        let token_id = parse_hex_id(&self.token_id).context("token_id hex")?;
        let signer_puddle_pubkey =
            parse_hex_id(&self.signer_puddle_pubkey).context("signer_puddle_pubkey hex")?;
        let sig_bytes = hex::decode(&self.signature).context("signature hex")?;
        if sig_bytes.len() != SIG_LEN {
            bail!("signature is {} bytes, expected {}", sig_bytes.len(), SIG_LEN);
        }
        let mut signature = [0u8; SIG_LEN];
        signature.copy_from_slice(&sig_bytes);
        Ok(RawRevoke {
            project_id,
            token_id,
            signer_puddle_pubkey,
            signature,
        })
    }

    /// Verify the Ed25519 signature on this envelope against
    /// `signer_puddle_pubkey`. Does NOT verify that the signer is an
    /// authorized admin — the caller must check that against the
    /// project's token tree.
    pub fn verify_signature(&self) -> Result<RawRevoke> {
        let raw = self.raw()?;
        let vk = VerifyingKey::from_bytes(&raw.signer_puddle_pubkey)
            .map_err(|e| anyhow!("invalid signer pubkey: {}", e))?;
        let sig = Signature::from_bytes(&raw.signature);
        let msg = canonical_revoke_bytes(
            &raw.project_id,
            &raw.token_id,
            self.revoked_at,
            &self.reason,
            &raw.signer_puddle_pubkey,
        );
        vk.verify(&msg, &sig)
            .map_err(|e| anyhow!("signature verification failed: {}", e))?;
        Ok(raw)
    }
}

/// A signed leave envelope. Self-leave only: signer == leaver.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct LeaveEnvelope {
    pub project_id: String,
    /// Hex-encoded puddle pubkey that is leaving (also the signer).
    pub leaving_puddle_pubkey: String,
    pub leave_time: i64,
    pub signature: String,
}

pub struct RawLeave {
    pub project_id: [u8; ID_LEN],
    pub leaving_puddle_pubkey: [u8; ID_LEN],
    pub signature: [u8; SIG_LEN],
}

impl LeaveEnvelope {
    pub fn build(
        signing_key: &SigningKey,
        project_id: &[u8; ID_LEN],
        leave_time: i64,
    ) -> Self {
        let leaving_pub: [u8; ID_LEN] = signing_key.verifying_key().to_bytes();
        let msg = canonical_leave_bytes(project_id, &leaving_pub, leave_time);
        let sig = signing_key.sign(&msg);
        Self {
            project_id: hex_id(project_id),
            leaving_puddle_pubkey: hex_id(&leaving_pub),
            leave_time,
            signature: hex::encode(sig.to_bytes()),
        }
    }

    /// Self-leave constructor with a delegated signer. See
    /// [`RevokeEnvelope::build_with`] for the rationale.
    pub fn build_with<F>(
        project_id: &[u8; ID_LEN],
        leaving_puddle_pubkey: &[u8; ID_LEN],
        leave_time: i64,
        sign: F,
    ) -> Result<Self>
    where
        F: FnOnce(&[u8]) -> Result<[u8; SIG_LEN]>,
    {
        let msg = canonical_leave_bytes(project_id, leaving_puddle_pubkey, leave_time);
        let sig = sign(&msg)?;
        let env = Self {
            project_id: hex_id(project_id),
            leaving_puddle_pubkey: hex_id(leaving_puddle_pubkey),
            leave_time,
            signature: hex::encode(sig),
        };
        env.verify_signature()
            .context("freshly-built leave envelope failed self-verification")?;
        Ok(env)
    }

    pub fn raw(&self) -> Result<RawLeave> {
        let project_id = parse_hex_id(&self.project_id).context("project_id hex")?;
        let leaving_puddle_pubkey =
            parse_hex_id(&self.leaving_puddle_pubkey).context("leaving_puddle_pubkey hex")?;
        let sig_bytes = hex::decode(&self.signature).context("signature hex")?;
        if sig_bytes.len() != SIG_LEN {
            bail!("signature is {} bytes, expected {}", sig_bytes.len(), SIG_LEN);
        }
        let mut signature = [0u8; SIG_LEN];
        signature.copy_from_slice(&sig_bytes);
        Ok(RawLeave {
            project_id,
            leaving_puddle_pubkey,
            signature,
        })
    }

    /// Verify the Ed25519 signature on this envelope against
    /// `leaving_puddle_pubkey` (self-leave: signer == leaver).
    pub fn verify_signature(&self) -> Result<RawLeave> {
        let raw = self.raw()?;
        let vk = VerifyingKey::from_bytes(&raw.leaving_puddle_pubkey)
            .map_err(|e| anyhow!("invalid leaver pubkey: {}", e))?;
        let sig = Signature::from_bytes(&raw.signature);
        let msg =
            canonical_leave_bytes(&raw.project_id, &raw.leaving_puddle_pubkey, self.leave_time);
        vk.verify(&msg, &sig)
            .map_err(|e| anyhow!("signature verification failed: {}", e))?;
        Ok(raw)
    }
}

/// A signed founder-rekey delegation (ADR-019).
///
/// Bridges the gap between `project_id`'s frozen anchor (the genesis
/// founder pubkey) and the founder's *current* signing key after one
/// or more puddle rekeys. The chain stack `bootstrap (anchored to
/// genesis) → delegation_0 (genesis signs rekey_1) → delegation_1
/// (rekey_1 signs rekey_2) → … → admin_token (rekey_N signs)` is
/// what `tokens::verify_chain` walks at request time.
///
/// **Threat-model wrinkle (load-bearing):** the OLD signer's key is by
/// definition still available *during* a rekey ceremony — ADR-018's
/// `rekey-pair` requires the OLD passphrase, so the founder can sign
/// this envelope inline. But that same property means an attacker who
/// has compromised the OLD key can also sign a forged delegation
/// pointing at *their* puddle pubkey. A delegation envelope on its
/// own therefore is **not authoritative**:
///
/// - **Single-puddle case** (founder = sole member of all members'
///   puddle): delegations are local-only artifacts, never
///   gossiped as authority. ADR-018's pair-claim out-of-band code
///   already gates puddle membership; gitmirror inherits that trust.
/// - **Multi-puddle case** (project members in foreign puddles): a
///   delegation becomes authoritative for member F only after F runs
///   `gitmirror project rejoin <vigo://...> <code>` with a fresh
///   capability code from the founder over an out-of-band channel
///   — same trust hop as the original `project invite`/`join`.
///
/// Receiving a delegation via the gossip rail (slice 4) is a *hint*
/// surfaced in `gitmirror status`, not a state change.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct DelegationEnvelope {
    /// Hex-encoded `sha256(genesis_founder_puddle_pubkey)` — frozen
    /// across all rekeys for this project.
    pub project_id: String,
    /// Hex-encoded puddle pubkey that signed THIS delegation. For the
    /// first delegation in a project's chain, equals the genesis
    /// founder pubkey baked into `project_id`. For subsequent
    /// delegations, equals the previous link's `new_founder_pubkey`.
    pub old_founder_pubkey: String,
    /// Hex-encoded puddle pubkey that this delegation grants founder
    /// authority to. Future tokens / further delegations chain
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
            project_id: hex_id(project_id),
            old_founder_pubkey: hex_id(&old_pub),
            new_founder_pubkey: hex_id(new_founder_pubkey),
            delegated_at,
            signature: hex::encode(sig.to_bytes()),
        }
    }

    /// Build with a delegated signer (e.g. the puddle session
    /// helper). Returns an envelope that has self-verified — bad
    /// caller-supplied signatures fail loudly here rather than at
    /// chain-walk time. See [`RevokeEnvelope::build_with`] for the
    /// rationale.
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
            project_id: hex_id(project_id),
            old_founder_pubkey: hex_id(old_founder_pubkey),
            new_founder_pubkey: hex_id(new_founder_pubkey),
            delegated_at,
            signature: hex::encode(sig),
        };
        env.verify_signature()
            .context("freshly-built delegation envelope failed self-verification")?;
        Ok(env)
    }

    /// Decode all hex fields into raw byte arrays.
    pub fn raw(&self) -> Result<RawDelegation> {
        let project_id = parse_hex_id(&self.project_id).context("project_id hex")?;
        let old_founder_pubkey = parse_hex_id(&self.old_founder_pubkey)
            .context("old_founder_pubkey hex")?;
        let new_founder_pubkey = parse_hex_id(&self.new_founder_pubkey)
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
    /// chain integrity — that's `tokens::verify_chain`'s job (slice 2).
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

// ---------- persistence ----------

/// Root envelopes directory for a project:
/// `<base>/envelopes/<project_id_hex>/`.
pub fn project_envelopes_dir(gitmirror_base: &Path, project_id: &[u8; ID_LEN]) -> PathBuf {
    gitmirror_base
        .join("envelopes")
        .join(hex_id(project_id))
}

pub fn revokes_dir(gitmirror_base: &Path, project_id: &[u8; ID_LEN]) -> PathBuf {
    project_envelopes_dir(gitmirror_base, project_id).join("revokes")
}

pub fn leaves_dir(gitmirror_base: &Path, project_id: &[u8; ID_LEN]) -> PathBuf {
    project_envelopes_dir(gitmirror_base, project_id).join("leaves")
}

pub fn revoke_path(
    gitmirror_base: &Path,
    project_id: &[u8; ID_LEN],
    token_id: &[u8; ID_LEN],
) -> PathBuf {
    revokes_dir(gitmirror_base, project_id).join(format!("{}.json", hex_id(token_id)))
}

pub fn leave_path(
    gitmirror_base: &Path,
    project_id: &[u8; ID_LEN],
    leaving_puddle_pubkey: &[u8; ID_LEN],
) -> PathBuf {
    leaves_dir(gitmirror_base, project_id)
        .join(format!("{}.json", hex_id(leaving_puddle_pubkey)))
}

/// Persist a REVOKE envelope. Idempotent: re-arrival of the same
/// envelope (or any envelope with `revoked_at <= existing`) is a
/// no-op. Returns `true` when the envelope changed disk state.
pub fn save_revoke(gitmirror_base: &Path, e: &RevokeEnvelope) -> Result<bool> {
    let raw = e.verify_signature().context("verify_signature before save")?;
    let path = revoke_path(gitmirror_base, &raw.project_id, &raw.token_id);
    if let Some(existing) = load_revoke_at(&path)? {
        if existing.revoked_at >= e.revoked_at {
            return Ok(false);
        }
    }
    write_envelope(&path, e)?;
    Ok(true)
}

/// Persist a LEAVE envelope. Idempotent semantics match
/// [`save_revoke`].
pub fn save_leave(gitmirror_base: &Path, e: &LeaveEnvelope) -> Result<bool> {
    let raw = e.verify_signature().context("verify_signature before save")?;
    let path = leave_path(gitmirror_base, &raw.project_id, &raw.leaving_puddle_pubkey);
    if let Some(existing) = load_leave_at(&path)? {
        if existing.leave_time >= e.leave_time {
            return Ok(false);
        }
    }
    write_envelope(&path, e)?;
    Ok(true)
}

fn write_envelope<T: Serialize>(path: &Path, payload: &T) -> Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .with_context(|| format!("create {}", parent.display()))?;
    }
    let json = serde_json::to_vec_pretty(payload).context("serialize envelope")?;
    let tmp = path.with_extension("json.tmp");
    {
        #[cfg(unix)]
        use std::os::unix::fs::OpenOptionsExt;
        let mut opts = fs::OpenOptions::new();
        opts.create(true).write(true).truncate(true);
        #[cfg(unix)]
        opts.mode(0o600);
        let mut f = opts
            .open(&tmp)
            .with_context(|| format!("create {}", tmp.display()))?;
        f.write_all(&json)?;
        f.sync_all().ok();
    }
    fs::rename(&tmp, path).with_context(|| format!("rename to {}", path.display()))?;
    Ok(())
}

fn load_revoke_at(path: &Path) -> Result<Option<RevokeEnvelope>> {
    match fs::read(path) {
        Ok(b) => match serde_json::from_slice(&b) {
            Ok(e) => Ok(Some(e)),
            Err(err) => {
                tracing::warn!(
                    path = %path.display(),
                    error = %err,
                    "gitmirror: malformed revoke envelope, treating as absent"
                );
                Ok(None)
            }
        },
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e).with_context(|| format!("read {}", path.display())),
    }
}

fn load_leave_at(path: &Path) -> Result<Option<LeaveEnvelope>> {
    match fs::read(path) {
        Ok(b) => match serde_json::from_slice(&b) {
            Ok(e) => Ok(Some(e)),
            Err(err) => {
                tracing::warn!(
                    path = %path.display(),
                    error = %err,
                    "gitmirror: malformed leave envelope, treating as absent"
                );
                Ok(None)
            }
        },
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e).with_context(|| format!("read {}", path.display())),
    }
}

/// True iff a current REVOKE envelope exists for `(project_id,
/// token_id)`. Does not distinguish "never revoked" from "envelope
/// present but signature invalid"; the daemon's gate-check side
/// validates signatures eagerly at receive time so a malformed file
/// here means the envelope was never accepted.
pub fn is_token_revoked(
    gitmirror_base: &Path,
    project_id: &[u8; ID_LEN],
    token_id: &[u8; ID_LEN],
) -> bool {
    revoke_path(gitmirror_base, project_id, token_id).exists()
}

/// Load all REVOKE envelopes for a project. Skips malformed files
/// with a warn-log; never errors on absence.
pub fn load_revokes(
    gitmirror_base: &Path,
    project_id: &[u8; ID_LEN],
) -> Result<Vec<RevokeEnvelope>> {
    let dir = revokes_dir(gitmirror_base, project_id);
    let entries = match fs::read_dir(&dir) {
        Ok(it) => it,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e).with_context(|| format!("read_dir {}", dir.display())),
    };
    let mut out = Vec::new();
    for entry in entries.flatten() {
        let p = entry.path();
        if p.extension().map(|x| x == "json").unwrap_or(false) {
            if let Some(env) = load_revoke_at(&p)? {
                out.push(env);
            }
        }
    }
    Ok(out)
}

/// Load all LEAVE envelopes for a project.
pub fn load_leaves(
    gitmirror_base: &Path,
    project_id: &[u8; ID_LEN],
) -> Result<Vec<LeaveEnvelope>> {
    let dir = leaves_dir(gitmirror_base, project_id);
    let entries = match fs::read_dir(&dir) {
        Ok(it) => it,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e).with_context(|| format!("read_dir {}", dir.display())),
    };
    let mut out = Vec::new();
    for entry in entries.flatten() {
        let p = entry.path();
        if p.extension().map(|x| x == "json").unwrap_or(false) {
            if let Some(env) = load_leave_at(&p)? {
                out.push(env);
            }
        }
    }
    Ok(out)
}

/// Drop envelopes whose timestamp is older than `now - retention_secs`
/// across all projects. Returns total count reaped. Best-effort: a
/// single failed unlink does not abort the sweep.
pub fn reap_all(gitmirror_base: &Path, now: i64, retention_secs: i64) -> Result<usize> {
    let envelopes_root = gitmirror_base.join("envelopes");
    let project_dirs = match fs::read_dir(&envelopes_root) {
        Ok(it) => it,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(0),
        Err(e) => return Err(e).with_context(|| format!("read_dir {}", envelopes_root.display())),
    };
    let cutoff = now - retention_secs;
    let mut reaped = 0;
    for project_entry in project_dirs.flatten() {
        let pdir = project_entry.path();
        if !pdir.is_dir() {
            continue;
        }
        for sub in &["revokes", "leaves"] {
            let sub_dir = pdir.join(sub);
            let entries = match fs::read_dir(&sub_dir) {
                Ok(it) => it,
                Err(_) => continue,
            };
            for entry in entries.flatten() {
                let p = entry.path();
                let ts = match *sub {
                    "revokes" => load_revoke_at(&p).ok().flatten().map(|e| e.revoked_at),
                    "leaves" => load_leave_at(&p).ok().flatten().map(|e| e.leave_time),
                    _ => None,
                };
                if let Some(ts) = ts {
                    if ts < cutoff {
                        if fs::remove_file(&p).is_ok() {
                            reaped += 1;
                        }
                    }
                }
            }
        }
    }
    Ok(reaped)
}

/// 16-byte aggregate digest of a project's revoke + leave records.
/// Sorted by filename so envoys carrying the same logical set produce
/// the same digest regardless of write order. Used by future gossip
/// catch-up to detect divergence cheaply.
pub fn project_digest(
    gitmirror_base: &Path,
    project_id: &[u8; ID_LEN],
) -> Result<[u8; DIGEST_LEN]> {
    let mut hasher = Sha256::new();
    let mut revokes = load_revokes(gitmirror_base, project_id)?;
    revokes.sort_by(|a, b| a.token_id.cmp(&b.token_id));
    for r in &revokes {
        hasher.update(b"R|");
        hasher.update(r.token_id.as_bytes());
        hasher.update(b"|");
        hasher.update(r.revoked_at.to_be_bytes());
        hasher.update(b"|");
        hasher.update(r.signature.as_bytes());
        hasher.update(b"\n");
    }
    let mut leaves = load_leaves(gitmirror_base, project_id)?;
    leaves.sort_by(|a, b| a.leaving_puddle_pubkey.cmp(&b.leaving_puddle_pubkey));
    for l in &leaves {
        hasher.update(b"L|");
        hasher.update(l.leaving_puddle_pubkey.as_bytes());
        hasher.update(b"|");
        hasher.update(l.leave_time.to_be_bytes());
        hasher.update(b"|");
        hasher.update(l.signature.as_bytes());
        hasher.update(b"\n");
    }
    let full = hasher.finalize();
    let mut out = [0u8; DIGEST_LEN];
    out.copy_from_slice(&full[..DIGEST_LEN]);
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

    fn pub_bytes(k: &SigningKey) -> [u8; ID_LEN] {
        k.verifying_key().to_bytes()
    }

    fn sample_pid() -> [u8; ID_LEN] {
        [0xABu8; ID_LEN]
    }

    fn sample_tid() -> [u8; ID_LEN] {
        [0x12u8; ID_LEN]
    }

    // ---- revoke ----

    #[test]
    fn revoke_signs_and_verifies() {
        let admin = fresh_key();
        let env = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 1_700_000_000, "compromised");
        let raw = env.verify_signature().expect("signature should verify");
        assert_eq!(raw.project_id, sample_pid());
        assert_eq!(raw.token_id, sample_tid());
        assert_eq!(raw.signer_puddle_pubkey, pub_bytes(&admin));
    }

    #[test]
    fn revoke_rejects_tampered_token_id() {
        let admin = fresh_key();
        let mut env = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 1_700_000_000, "");
        env.token_id = hex_id(&[0xFFu8; ID_LEN]);
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn revoke_rejects_tampered_reason() {
        let admin = fresh_key();
        let mut env = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 1_700_000_000, "good");
        env.reason = "good ".to_string();
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn revoke_rejects_tampered_signer() {
        let admin = fresh_key();
        let other = fresh_key();
        let mut env = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 1_700_000_000, "");
        env.signer_puddle_pubkey = hex_id(&pub_bytes(&other));
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn revoke_persistence_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let admin = fresh_key();
        let env = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 1_700_000_000, "");
        let changed = save_revoke(dir.path(), &env).unwrap();
        assert!(changed);
        let loaded = load_revokes(dir.path(), &sample_pid()).unwrap();
        assert_eq!(loaded.len(), 1);
        assert_eq!(loaded[0], env);
        assert!(is_token_revoked(dir.path(), &sample_pid(), &sample_tid()));
    }

    #[test]
    fn revoke_save_idempotent_same_timestamp() {
        let dir = tempfile::tempdir().unwrap();
        let admin = fresh_key();
        let env = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 1_700_000_000, "");
        assert!(save_revoke(dir.path(), &env).unwrap());
        assert!(!save_revoke(dir.path(), &env).unwrap());
    }

    #[test]
    fn revoke_save_advances_with_newer_timestamp() {
        let dir = tempfile::tempdir().unwrap();
        let admin = fresh_key();
        let older = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 1_700_000_000, "v1");
        let newer = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 1_700_000_010, "v2");
        save_revoke(dir.path(), &older).unwrap();
        assert!(save_revoke(dir.path(), &newer).unwrap());
        let loaded = load_revokes(dir.path(), &sample_pid()).unwrap();
        assert_eq!(loaded.len(), 1);
        assert_eq!(loaded[0].revoked_at, 1_700_000_010);
        assert_eq!(loaded[0].reason, "v2");
    }

    #[test]
    fn revoke_save_rejects_older_timestamp() {
        let dir = tempfile::tempdir().unwrap();
        let admin = fresh_key();
        let older = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 1_700_000_000, "v1");
        let newer = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 1_700_000_010, "v2");
        save_revoke(dir.path(), &newer).unwrap();
        assert!(!save_revoke(dir.path(), &older).unwrap());
    }

    // ---- leave ----

    #[test]
    fn leave_signs_and_verifies() {
        let leaver = fresh_key();
        let env = LeaveEnvelope::build(&leaver, &sample_pid(), 1_700_000_000);
        let raw = env.verify_signature().expect("signature should verify");
        assert_eq!(raw.project_id, sample_pid());
        assert_eq!(raw.leaving_puddle_pubkey, pub_bytes(&leaver));
    }

    #[test]
    fn leave_rejects_tampered_pubkey() {
        let leaver = fresh_key();
        let other = fresh_key();
        let mut env = LeaveEnvelope::build(&leaver, &sample_pid(), 1_700_000_000);
        env.leaving_puddle_pubkey = hex_id(&pub_bytes(&other));
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn leave_rejects_tampered_time() {
        let leaver = fresh_key();
        let mut env = LeaveEnvelope::build(&leaver, &sample_pid(), 1_700_000_000);
        env.leave_time += 1;
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn leave_persistence_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let leaver = fresh_key();
        let env = LeaveEnvelope::build(&leaver, &sample_pid(), 1_700_000_000);
        save_leave(dir.path(), &env).unwrap();
        let loaded = load_leaves(dir.path(), &sample_pid()).unwrap();
        assert_eq!(loaded.len(), 1);
        assert_eq!(loaded[0], env);
    }

    #[test]
    fn leave_save_idempotent_same_timestamp() {
        let dir = tempfile::tempdir().unwrap();
        let leaver = fresh_key();
        let env = LeaveEnvelope::build(&leaver, &sample_pid(), 1_700_000_000);
        assert!(save_leave(dir.path(), &env).unwrap());
        assert!(!save_leave(dir.path(), &env).unwrap());
    }

    // ---- domain separation ----

    #[test]
    fn revoke_signature_does_not_satisfy_leave() {
        let key = fresh_key();
        let leave_msg = canonical_leave_bytes(&sample_pid(), &pub_bytes(&key), 1_700_000_000);
        let revoke_msg = canonical_revoke_bytes(
            &sample_pid(),
            &sample_tid(),
            1_700_000_000,
            "",
            &pub_bytes(&key),
        );
        // Different domain prefix → different leading bytes → distinct hashes.
        assert!(!leave_msg.starts_with(REVOKE_DOMAIN));
        assert!(!revoke_msg.starts_with(LEAVE_DOMAIN));
    }

    // ---- reap ----

    #[test]
    fn reap_drops_expired() {
        let dir = tempfile::tempdir().unwrap();
        let admin = fresh_key();
        let leaver = fresh_key();
        let old = RevokeEnvelope::build(&admin, &sample_pid(), &sample_tid(), 100, "");
        let fresh = RevokeEnvelope::build(
            &admin,
            &sample_pid(),
            &[0x99u8; ID_LEN],
            9_500,
            "",
        );
        let old_leave = LeaveEnvelope::build(&leaver, &sample_pid(), 100);
        save_revoke(dir.path(), &old).unwrap();
        save_revoke(dir.path(), &fresh).unwrap();
        save_leave(dir.path(), &old_leave).unwrap();
        // now=10_000, retention=1_000 → cutoff=9_000 → fresh kept, two olds reaped.
        let n = reap_all(dir.path(), 10_000, 1_000).unwrap();
        assert_eq!(n, 2);
        let r = load_revokes(dir.path(), &sample_pid()).unwrap();
        assert_eq!(r.len(), 1);
        assert_eq!(r[0].token_id, hex_id(&[0x99u8; ID_LEN]));
        assert!(load_leaves(dir.path(), &sample_pid()).unwrap().is_empty());
    }

    #[test]
    fn reap_on_missing_root_is_no_op() {
        let dir = tempfile::tempdir().unwrap();
        // No envelopes/ root exists yet — reap returns 0 without error.
        assert_eq!(reap_all(dir.path(), 10_000, 1_000).unwrap(), 0);
    }

    // ---- digest ----

    #[test]
    fn digest_is_stable_across_write_order() {
        let dir_a = tempfile::tempdir().unwrap();
        let dir_b = tempfile::tempdir().unwrap();
        let admin = fresh_key();
        let r1 = RevokeEnvelope::build(&admin, &sample_pid(), &[0x01u8; ID_LEN], 100, "");
        let r2 = RevokeEnvelope::build(&admin, &sample_pid(), &[0x02u8; ID_LEN], 200, "");
        save_revoke(dir_a.path(), &r1).unwrap();
        save_revoke(dir_a.path(), &r2).unwrap();
        save_revoke(dir_b.path(), &r2).unwrap();
        save_revoke(dir_b.path(), &r1).unwrap();
        assert_eq!(
            project_digest(dir_a.path(), &sample_pid()).unwrap(),
            project_digest(dir_b.path(), &sample_pid()).unwrap()
        );
    }

    #[test]
    fn digest_changes_when_record_added() {
        let dir = tempfile::tempdir().unwrap();
        let admin = fresh_key();
        let d0 = project_digest(dir.path(), &sample_pid()).unwrap();
        let r1 = RevokeEnvelope::build(&admin, &sample_pid(), &[0x01u8; ID_LEN], 100, "");
        save_revoke(dir.path(), &r1).unwrap();
        let d1 = project_digest(dir.path(), &sample_pid()).unwrap();
        assert_ne!(d0, d1);
    }

    // ---- delegation (ADR-019) ----

    #[test]
    fn delegation_signs_and_verifies() {
        let old_key = fresh_key();
        let new_key = fresh_key();
        let env = DelegationEnvelope::build(
            &old_key,
            &sample_pid(),
            &pub_bytes(&new_key),
            1_700_000_000,
        );
        let raw = env.verify_signature().expect("signature should verify");
        assert_eq!(raw.project_id, sample_pid());
        assert_eq!(raw.old_founder_pubkey, pub_bytes(&old_key));
        assert_eq!(raw.new_founder_pubkey, pub_bytes(&new_key));
    }

    #[test]
    fn delegation_rejects_tampered_new_pubkey() {
        let old_key = fresh_key();
        let new_key = fresh_key();
        let attacker = fresh_key();
        let mut env = DelegationEnvelope::build(
            &old_key,
            &sample_pid(),
            &pub_bytes(&new_key),
            1_700_000_000,
        );
        env.new_founder_pubkey = hex_id(&pub_bytes(&attacker));
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn delegation_rejects_tampered_old_pubkey() {
        let old_key = fresh_key();
        let new_key = fresh_key();
        let attacker = fresh_key();
        let mut env = DelegationEnvelope::build(
            &old_key,
            &sample_pid(),
            &pub_bytes(&new_key),
            1_700_000_000,
        );
        // Re-pointing the signer to a different key turns the
        // signature into "verified against the wrong pubkey" → fail.
        env.old_founder_pubkey = hex_id(&pub_bytes(&attacker));
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn delegation_rejects_tampered_timestamp() {
        let old_key = fresh_key();
        let new_key = fresh_key();
        let mut env = DelegationEnvelope::build(
            &old_key,
            &sample_pid(),
            &pub_bytes(&new_key),
            1_700_000_000,
        );
        env.delegated_at += 1;
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn delegation_rejects_tampered_project_id() {
        let old_key = fresh_key();
        let new_key = fresh_key();
        let mut env = DelegationEnvelope::build(
            &old_key,
            &sample_pid(),
            &pub_bytes(&new_key),
            1_700_000_000,
        );
        env.project_id = hex_id(&[0xCDu8; ID_LEN]);
        assert!(env.verify_signature().is_err());
    }

    #[test]
    fn delegation_build_with_self_verifies() {
        // Mirror the cryptsync session-helper signing path: caller
        // owns the signing oracle, build_with self-verifies before
        // returning.
        let old_key = fresh_key();
        let new_key = fresh_key();
        let env = DelegationEnvelope::build_with(
            &sample_pid(),
            &pub_bytes(&old_key),
            &pub_bytes(&new_key),
            1_700_000_000,
            |msg| Ok(old_key.sign(msg).to_bytes()),
        )
        .expect("build_with should succeed and self-verify");
        env.verify_signature().expect("post-build verification");
    }

    #[test]
    fn delegation_build_with_rejects_bad_signer_oracle() {
        // Oracle returns a sig from the WRONG key — build_with's
        // self-verify must catch this before returning.
        let old_key = fresh_key();
        let new_key = fresh_key();
        let attacker = fresh_key();
        let result = DelegationEnvelope::build_with(
            &sample_pid(),
            &pub_bytes(&old_key),
            &pub_bytes(&new_key),
            1_700_000_000,
            |msg| Ok(attacker.sign(msg).to_bytes()),
        );
        assert!(
            result.is_err(),
            "build_with must reject a signature that doesn't match old_founder_pubkey"
        );
    }

    // ---- domain separation: delegation signature does not satisfy other envelopes ----

    #[test]
    fn delegation_signature_does_not_satisfy_revoke_or_leave() {
        let key = fresh_key();
        let other = fresh_key();
        let delegation_msg = canonical_delegation_bytes(
            &sample_pid(),
            &pub_bytes(&key),
            &pub_bytes(&other),
            1_700_000_000,
        );
        let revoke_msg = canonical_revoke_bytes(
            &sample_pid(),
            &sample_tid(),
            1_700_000_000,
            "",
            &pub_bytes(&key),
        );
        let leave_msg = canonical_leave_bytes(&sample_pid(), &pub_bytes(&key), 1_700_000_000);
        // Each domain prefix is unique to its envelope type; canonical
        // bytes for one shape must not start with another's prefix.
        assert!(delegation_msg.starts_with(DELEGATION_DOMAIN));
        assert!(!delegation_msg.starts_with(REVOKE_DOMAIN));
        assert!(!delegation_msg.starts_with(LEAVE_DOMAIN));
        assert!(!revoke_msg.starts_with(DELEGATION_DOMAIN));
        assert!(!leave_msg.starts_with(DELEGATION_DOMAIN));
    }
}
