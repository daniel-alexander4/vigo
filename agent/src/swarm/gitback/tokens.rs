// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Capability tokens for unified-substrate gitback (ADR-015).
//!
//! Membership in a project is a stack of Ed25519-signed capability
//! tokens, anchored to the founder's puddle pubkey. The receiving
//! envoy verifies an incoming push by walking the chain of tokens
//! the caller presents back to the project's anchor:
//!
//! ```text
//!  founder puddle P0  ──signs──▶  bootstrap admin token T0 (subject = F0)
//!                                             │
//!                                             │ admin T0 signs ▼
//!                                  member token T1 (subject = F1, role = member)
//! ```
//!
//! The token bytes signed by the issuer are a hand-rolled
//! deterministic encoding (matching lockbox's leaves.rs pattern,
//! never JSON whitespace). Token ID is the sha256 of those exact
//! signed bytes — content-addressed, so revocation envelopes can name
//! a token by id without ambiguity.
//!
//! Phase 1 of ADR-015 (this module) ships the cryptographic
//! primitives: token shape, canonical encoding, signing,
//! single-token + chain verification. Persistence (`~/.vigo-gitback/
//! <project_id>/`), gossip, revocation, and CLI verbs land in
//! subsequent commits of the arc.

use anyhow::{anyhow, bail, Context, Result};
use ed25519_dalek::{Signer, SigningKey, Verifier, VerifyingKey};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

/// Domain-separator prefix for canonical signed bytes. Bumped if the
/// token shape ever changes. Receivers reject signatures over a
/// different domain even with otherwise-matching key material —
/// prevents a token from one Vigo subsystem being confused for
/// another in a future where multiple subsystems sign Ed25519
/// envelopes.
pub const TOKEN_DOMAIN: &[u8] = b"gitback-token-v2\0";

/// Length of puddle pubkeys + token IDs (raw Ed25519 pubkey + sha256).
pub const ID_LEN: usize = 32;

/// Length of an Ed25519 signature.
pub const SIG_LEN: usize = 64;

/// Sentinel value carried in the canonical bytes when a token has no
/// parent (founder's bootstrap token). Distinct from any real token id
/// because sha256 over non-empty inputs never returns all zeros.
pub const NO_PARENT: [u8; ID_LEN] = [0u8; ID_LEN];

/// Sentinel for "never expires" in the canonical bytes. Matches the
/// `expires_at: None` variant of the in-memory body. Real expirations
/// must be > 0 unix-seconds, which they always are after 1970.
pub const NEVER_EXPIRES: i64 = 0;

/// Roles a token can grant. Admin can mint further tokens; member can
/// fetch + push on a project but not invite or revoke.
///
/// The on-wire encoding is one byte: `0x00` admin, `0x01` member.
/// Adding a third role bumps the wire byte without breaking existing
/// chains as long as `decode` keeps tolerating the old values.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum Role {
    Admin,
    Member,
}

impl Role {
    fn as_byte(self) -> u8 {
        match self {
            Role::Admin => 0x00,
            Role::Member => 0x01,
        }
    }

    /// Inverse of `as_byte`. Reserved for future on-wire decoders;
    /// today the canonical encoding is one-way (we sign + hash over
    /// it, never reconstruct from it). Tests exercise it to lock in
    /// the byte mapping.
    #[allow(dead_code)]
    fn from_byte(b: u8) -> Result<Self> {
        match b {
            0x00 => Ok(Role::Admin),
            0x01 => Ok(Role::Member),
            other => bail!("unknown role byte 0x{:02x}", other),
        }
    }
}

/// Body of a capability token. The signed bytes are a deterministic
/// encoding of these fields; the `Token` envelope wraps body +
/// signature for transport / persistence.
///
/// Field order in the canonical bytes (TOKEN_DOMAIN prefix, then):
/// 1. project_id (32B)
/// 2. handle (2B BE length + UTF-8 bytes)
/// 3. subject_puddle_pubkey (32B)
/// 4. role (1B)
/// 5. issued_at (8B BE i64)
/// 6. expires_at (8B BE i64, 0 = NEVER_EXPIRES)
/// 7. signer_puddle_pubkey (32B)
/// 8. parent_token_id (32B, NO_PARENT when None)
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TokenBody {
    /// Project anchor. `project_id = sha256(founder_puddle_pubkey ‖ handle)`
    /// — content-addressed; the URL is `gitback://<project_id>/<handle>`.
    /// One puddle can found multiple projects (one per repo); each handle
    /// yields a distinct project_id, so handles can't be squatted.
    pub project_id: [u8; ID_LEN],
    /// The project's human-readable handle. Committed to here so the
    /// bootstrap token's `sha256(signer_puddle_pubkey ‖ handle)`
    /// reproduces `project_id` — that binding is what stops a third
    /// party minting a chain for a `project_id` they don't own.
    /// Identical across every token in a chain.
    pub handle: String,
    /// Puddle pubkey this token grants access to. Holders of puddle
    /// F's wrapped key (any envoy that ran `puddle unlock` /
    /// `join`) can push under a token whose subject is F.
    pub subject_puddle_pubkey: [u8; ID_LEN],
    /// Role granted to the subject puddle.
    pub role: Role,
    /// Unix seconds when this token was issued.
    pub issued_at: i64,
    /// Unix seconds when this token stops being valid. `None`
    /// (encoded as `0`) means no expiry — the founder's bootstrap
    /// token typically has no expiry; member tokens often do.
    pub expires_at: Option<i64>,
    /// Pubkey of the puddle that signed this token. For the
    /// founder's bootstrap token, this equals the founder pubkey
    /// (= sha256-input of project_id). For derived tokens, this
    /// is the admin's subject_puddle_pubkey.
    pub signer_puddle_pubkey: [u8; ID_LEN],
    /// ID of the admin token used to sign this one. `None` for the
    /// founder's bootstrap token (signed directly by the project's
    /// anchor pubkey).
    pub parent_token_id: Option<[u8; ID_LEN]>,
}

/// A signed capability token: body + Ed25519 signature over the
/// canonical bytes of that body.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Token {
    pub body: TokenBody,
    /// Raw 64-byte Ed25519 signature.
    #[serde(with = "serde_signature")]
    pub signature: [u8; SIG_LEN],
}

/// Hand-rolled deterministic encoding for `TokenBody`. NEVER mix this
/// with serde — JSON whitespace and field-order ambiguity would
/// silently produce two different signed messages for what looks
/// like the same body.
pub fn canonical_bytes(body: &TokenBody) -> Vec<u8> {
    let handle_bytes = body.handle.as_bytes();
    let mut out =
        Vec::with_capacity(TOKEN_DOMAIN.len() + 32 + 2 + handle_bytes.len() + 32 + 1 + 8 + 8 + 32 + 32);
    out.extend_from_slice(TOKEN_DOMAIN);
    out.extend_from_slice(&body.project_id);
    // Handle: 2-byte BE length prefix so `pubkey ‖ handle` (and the
    // surrounding fields) parse unambiguously regardless of handle
    // content. `validate_handle` caps the length at 64; the u16 prefix
    // is defense in depth, not a constraint relaxation.
    out.extend_from_slice(&(handle_bytes.len() as u16).to_be_bytes());
    out.extend_from_slice(handle_bytes);
    out.extend_from_slice(&body.subject_puddle_pubkey);
    out.push(body.role.as_byte());
    out.extend_from_slice(&body.issued_at.to_be_bytes());
    out.extend_from_slice(&body.expires_at.unwrap_or(NEVER_EXPIRES).to_be_bytes());
    out.extend_from_slice(&body.signer_puddle_pubkey);
    out.extend_from_slice(&body.parent_token_id.unwrap_or(NO_PARENT));
    out
}

/// `project_id = sha256(founder_puddle_pubkey ‖ handle)`. Stable across
/// all envoys; baked into the URL `gitback://<project_id>/<handle>`.
/// Including the handle lets one puddle found multiple projects (one per
/// repo) — each handle yields a distinct project_id. The pubkey is
/// fixed-width (32 B), so `pubkey ‖ handle_bytes` is unambiguous without
/// a separator.
pub fn project_id_for(founder_puddle_pubkey: &[u8; ID_LEN], handle: &str) -> [u8; ID_LEN] {
    let mut hasher = Sha256::new();
    hasher.update(founder_puddle_pubkey);
    hasher.update(handle.as_bytes());
    let digest = hasher.finalize();
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&digest);
    out
}

impl Token {
    /// Token ID = sha256 of the canonical signed bytes. Stable;
    /// independent of the signature. Two equivalent bodies always
    /// share the same ID.
    pub fn id(&self) -> [u8; ID_LEN] {
        let bytes = canonical_bytes(&self.body);
        let mut hasher = Sha256::new();
        hasher.update(&bytes);
        let digest = hasher.finalize();
        let mut out = [0u8; ID_LEN];
        out.copy_from_slice(&digest);
        out
    }

    /// Issue + sign a token. The caller supplies the signing key (an
    /// Ed25519 private key the issuing puddle holds) and the body.
    /// The body's `signer_puddle_pubkey` MUST equal the public half of
    /// `signing_key`; we re-check here so a misuse fails loudly
    /// instead of producing a body that won't verify.
    pub fn issue(signing_key: &SigningKey, body: TokenBody) -> Result<Token> {
        let derived_pub = signing_key.verifying_key().to_bytes();
        if derived_pub != body.signer_puddle_pubkey {
            bail!(
                "signing key does not match body.signer_puddle_pubkey \
                 (key {:x?}, body {:x?})",
                &derived_pub[..4],
                &body.signer_puddle_pubkey[..4]
            );
        }
        let bytes = canonical_bytes(&body);
        let sig = signing_key.sign(&bytes);
        Ok(Token {
            body,
            signature: sig.to_bytes(),
        })
    }

    /// Verify the signature on this token against the embedded
    /// `signer_puddle_pubkey`. Does NOT verify the chain — call
    /// [`verify_chain`] for full anchor-rooted verification.
    pub fn verify_signature(&self) -> Result<()> {
        let bytes = canonical_bytes(&self.body);
        let vk = VerifyingKey::from_bytes(&self.body.signer_puddle_pubkey)
            .map_err(|e| anyhow!("invalid signer pubkey: {}", e))?;
        let sig = ed25519_dalek::Signature::from_bytes(&self.signature);
        vk.verify(&bytes, &sig)
            .map_err(|e| anyhow!("signature verification failed: {}", e))
    }
}

/// Why a chain failed to verify. Carries enough context to log a
/// useful error and (later) drive UX in the helper daemon.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ChainError {
    /// Caller supplied an empty chain.
    Empty,
    /// The first (deepest) token's parent_token_id was set; the root
    /// of a chain MUST be a founder bootstrap token with no parent.
    RootHasParent,
    /// The first token's signer_puddle_pubkey didn't match the project's
    /// founder pubkey (project_id mismatch caught earlier; this catches
    /// a token signed by someone other than the founder claiming to be
    /// a root).
    RootNotFounder,
    /// A non-root token had its parent_token_id set to a value that
    /// didn't match the previous token in the chain.
    ParentMismatch { index: usize },
    /// A non-root token's signer_puddle_pubkey didn't match the
    /// subject_puddle_pubkey of the parent token (whoever signed this
    /// link wasn't the parent's grantee).
    SignerNotParentSubject { index: usize },
    /// A non-root token was signed by a parent whose role wasn't
    /// `Admin` — only admins can mint further tokens.
    ParentNotAdmin { index: usize },
    /// A token's `project_id` differed from the chain's anchor.
    ProjectIdMismatch { index: usize },
    /// Signature on a token failed to verify.
    BadSignature { index: usize, msg: String },
    /// A token's `expires_at` was in the past relative to `now`.
    Expired { index: usize, expires_at: i64 },
    /// A token's `issued_at` was after its parent's `issued_at`?
    /// Currently unenforced — chains can have arbitrary issuance order
    /// as long as signatures hold. Reserved for future.
    #[allow(dead_code)]
    IssuanceOrder { index: usize },
    // ----- ADR-019 founder-rekey delegation chain -----
    /// Delegation list exceeds [`super::envelopes::DELEGATION_MAX_LEN`].
    /// Pre-rotation hook in `cmd_rekey_pair` should have aborted the
    /// rekey before reaching this state; encountering it at verify
    /// time means a peer presented a chain we'd never have signed.
    DelegationCapExceeded { len: usize },
    /// Delegation envelope failed to decode at `index` (bad hex,
    /// wrong-length signature, etc.).
    DelegationDecode { index: usize, msg: String },
    /// Delegation's `project_id` doesn't match the chain anchor.
    DelegationProjectIdMismatch { index: usize },
    /// Delegation's `old_founder_pubkey` doesn't equal the previous
    /// link's `new_founder_pubkey` (or, for the first delegation,
    /// the genesis founder pubkey baked into `project_id`).
    DelegationLinkBroken { index: usize },
    /// Delegation timestamps must be strictly increasing — `delegated_at`
    /// at `index` is `<=` the previous link's. A reordered or
    /// duplicate-timestamp chain is rejected to close a subtle attack
    /// where an attacker swaps in their own envelope at the same
    /// instant as a legitimate rotation.
    DelegationOrderBroken { index: usize },
    /// Delegation Ed25519 signature verification failed.
    DelegationBadSignature { index: usize, msg: String },
    /// Token at `index` was signed by a pubkey that wasn't the
    /// authoritative founder identity for the token's `issued_at`.
    /// Closes the deposed-founder attack (a retired founder's
    /// compromised key signs new tokens with `issued_at` past their
    /// authority window).
    SignerOutsideFounderWindow { index: usize, signer_prefix: String },
}

impl std::fmt::Display for ChainError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ChainError::Empty => write!(f, "chain is empty"),
            ChainError::RootHasParent => {
                write!(f, "root token has parent_token_id set; root must be a founder bootstrap token")
            }
            ChainError::RootNotFounder => write!(
                f,
                "root token's signer is not the project's founder puddle pubkey"
            ),
            ChainError::ParentMismatch { index } => write!(
                f,
                "token {} has parent_token_id != id(token[{}-1])",
                index, index
            ),
            ChainError::SignerNotParentSubject { index } => write!(
                f,
                "token {}'s signer is not the subject of token[{}-1]",
                index, index
            ),
            ChainError::ParentNotAdmin { index } => write!(
                f,
                "token {} was signed by a parent token whose role is not Admin",
                index
            ),
            ChainError::ProjectIdMismatch { index } => write!(
                f,
                "token {}'s project_id differs from the chain anchor",
                index
            ),
            ChainError::BadSignature { index, msg } => {
                write!(f, "token {} signature: {}", index, msg)
            }
            ChainError::Expired { index, expires_at } => write!(
                f,
                "token {} expired at unix-ts {}",
                index, expires_at
            ),
            ChainError::IssuanceOrder { index } => {
                write!(f, "token {} issued before its parent", index)
            }
            ChainError::DelegationCapExceeded { len } => write!(
                f,
                "delegation chain length {} exceeds cap {}",
                len,
                super::envelopes::DELEGATION_MAX_LEN
            ),
            ChainError::DelegationDecode { index, msg } => write!(
                f,
                "delegation {} decode failed: {}",
                index, msg
            ),
            ChainError::DelegationProjectIdMismatch { index } => write!(
                f,
                "delegation {} has project_id != chain anchor",
                index
            ),
            ChainError::DelegationLinkBroken { index } => write!(
                f,
                "delegation {}'s old_founder_pubkey doesn't match the previous link",
                index
            ),
            ChainError::DelegationOrderBroken { index } => write!(
                f,
                "delegation {}'s delegated_at is not strictly increasing",
                index
            ),
            ChainError::DelegationBadSignature { index, msg } => write!(
                f,
                "delegation {} signature: {}",
                index, msg
            ),
            ChainError::SignerOutsideFounderWindow { index, signer_prefix } => write!(
                f,
                "token {} signed by {} outside its authority window for the token's issued_at",
                index, signer_prefix
            ),
        }
    }
}

impl std::error::Error for ChainError {}

/// Verify a token chain against the project's genesis founder pubkey
/// at a given `now` (unix seconds — pass `now_secs()` in production;
/// tests inject a fixed value).
///
/// **Chain layout:**
/// - `chain[0]` is the founder's bootstrap token (parent = None,
///   signer = `founder_puddle_pubkey` — the GENESIS founder, baked into
///   `project_id`). Always.
/// - Each subsequent `chain[i]` was signed by `chain[i-1]` (whose
///   role MUST be Admin for it to have minted children), with one
///   exception: when `chain[i-1]` IS the bootstrap, `chain[i]`'s
///   signer may be ANY founder pubkey whose authority window covers
///   `chain[i].issued_at` — that includes the genesis pubkey directly,
///   AND every `new_founder_pubkey` named by a delegation in
///   `delegations`. This is what lets post-rekey admin tokens
///   chain back through a `bootstrap (signed by genesis) → admin
///   (signed by current founder)` link without re-minting the
///   bootstrap (which would change `project_id` and break URLs).
/// - `chain[chain.len()-1]` is the "leaf" — the token presented by
///   the caller for authorization.
///
/// **Delegation chain (ADR-019):**
///
/// `delegations` is the project's append-only list of founder rekeys
/// (`DelegationChain::delegations`). Empty = the founder hasn't
/// rekeyed; verifier behaves identically to pre-ADR-019. Non-empty
/// = each entry signed by the previous founder, naming the next.
/// Cap is [`super::envelopes::DELEGATION_MAX_LEN`]; warn band starts
/// at [`super::envelopes::DELEGATION_WARN_LEN`].
///
/// **Authority windows.** Each founder pubkey is authoritative over
/// a half-open interval `[start, end)`:
/// - genesis: `[i64::MIN, delegations[0].delegated_at)` (or `[i64::MIN,
///   i64::MAX)` if `delegations` is empty).
/// - `delegations[i].new_founder_pubkey`: `[delegations[i].delegated_at,
///   delegations[i+1].delegated_at)` (or `[delegations[i].delegated_at,
///   i64::MAX)` if it's the last delegation).
///
/// Tokens whose parent is the bootstrap MUST have `issued_at` falling
/// inside the window of their declared signer. This closes the
/// deposed-founder attack: an attacker holding a retired founder's
/// key cannot sign new tokens dated past that founder's window.
///
/// On Ok, the leaf's role + subject_puddle_pubkey are the
/// authorization context the caller is asserting.
pub fn verify_chain(
    project_id: &[u8; ID_LEN],
    founder_puddle_pubkey: &[u8; ID_LEN],
    delegations: &[super::envelopes::DelegationEnvelope],
    chain: &[Token],
    now: i64,
) -> std::result::Result<(), ChainError> {
    if chain.is_empty() {
        return Err(ChainError::Empty);
    }

    // The `project_id == sha256(founder_puddle_pubkey ‖ handle)`
    // binding is checked below, after the bootstrap token's signature is
    // verified — we need the handle from the (signature-verified)
    // bootstrap body, and binding it any earlier would trust an
    // unverified field.

    // ---- Pre-walk: validate the delegation chain itself. ----
    if delegations.len() > super::envelopes::DELEGATION_MAX_LEN {
        return Err(ChainError::DelegationCapExceeded { len: delegations.len() });
    }
    // Window starts indexed by founder pubkey, in genesis-first order.
    // Each entry is `(founder_pubkey, window_start)`. window_end is
    // implied by the *next* entry's window_start (or i64::MAX for the
    // tail).
    let mut founder_windows: Vec<([u8; ID_LEN], i64)> = Vec::with_capacity(delegations.len() + 1);
    founder_windows.push((*founder_puddle_pubkey, i64::MIN));
    let mut prev_new_founder: [u8; ID_LEN] = *founder_puddle_pubkey;
    let mut prev_delegated_at: i64 = i64::MIN;
    for (i, env) in delegations.iter().enumerate() {
        let raw = env.raw().map_err(|e| ChainError::DelegationDecode {
            index: i,
            msg: e.to_string(),
        })?;
        if raw.project_id != *project_id {
            return Err(ChainError::DelegationProjectIdMismatch { index: i });
        }
        if raw.old_founder_pubkey != prev_new_founder {
            return Err(ChainError::DelegationLinkBroken { index: i });
        }
        if env.delegated_at <= prev_delegated_at {
            return Err(ChainError::DelegationOrderBroken { index: i });
        }
        env.verify_signature()
            .map_err(|e| ChainError::DelegationBadSignature {
                index: i,
                msg: e.to_string(),
            })?;
        founder_windows.push((raw.new_founder_pubkey, env.delegated_at));
        prev_new_founder = raw.new_founder_pubkey;
        prev_delegated_at = env.delegated_at;
    }

    // Helper: look up the founder pubkey authoritative at `at`.
    // Returns the LAST entry whose `window_start <= at`. Stable: a
    // token issued exactly at delegated_at falls under the new founder
    // (the boundary is half-open `[start, end)`).
    let founder_at = |at: i64| -> [u8; ID_LEN] {
        let mut authoritative = founder_windows[0].0;
        for (pk, start) in &founder_windows {
            if *start <= at {
                authoritative = *pk;
            } else {
                break;
            }
        }
        authoritative
    };

    // ---- Bootstrap (chain[0]) checks. ----
    let root = &chain[0];
    if root.body.parent_token_id.is_some() {
        return Err(ChainError::RootHasParent);
    }
    // Bootstrap is ALWAYS signed by genesis — never by a post-rekey
    // founder. The bootstrap is immutable; rekey doesn't re-mint it.
    if &root.body.signer_puddle_pubkey != founder_puddle_pubkey {
        return Err(ChainError::RootNotFounder);
    }
    if &root.body.project_id != project_id {
        return Err(ChainError::ProjectIdMismatch { index: 0 });
    }
    root.verify_signature()
        .map_err(|e| ChainError::BadSignature {
            index: 0,
            msg: e.to_string(),
        })?;
    expiry_check(root, 0, now)?;
    // Now that the bootstrap's signature is verified and its signer is
    // confirmed to be the genesis founder pubkey, the project_id MUST be
    // sha256(genesis founder pubkey ‖ bootstrap handle). This binding is
    // what stops a third party minting a chain claiming a project_id
    // derived from someone else's pubkey + handle.
    if &project_id_for(founder_puddle_pubkey, &root.body.handle) != project_id {
        return Err(ChainError::RootNotFounder);
    }

    // ---- Walk the rest of the chain. ----
    for i in 1..chain.len() {
        let parent = &chain[i - 1];
        let cur = &chain[i];

        if &cur.body.project_id != project_id {
            return Err(ChainError::ProjectIdMismatch { index: i });
        }
        // Every token in a chain carries the same handle as the
        // bootstrap. A mismatch can't break the binding (project_id is
        // the cryptographic key) but is a tamper signal — reject it.
        if cur.body.handle != root.body.handle {
            return Err(ChainError::ProjectIdMismatch { index: i });
        }
        if parent.body.role != Role::Admin {
            return Err(ChainError::ParentNotAdmin { index: i });
        }
        let parent_id = parent.id();
        match cur.body.parent_token_id {
            Some(p) if p == parent_id => {}
            _ => return Err(ChainError::ParentMismatch { index: i }),
        }

        // ADR-019: extended signer rule when the parent is the
        // bootstrap. Allow any founder pubkey whose authority window
        // covers cur.issued_at; otherwise the standard parent.subject
        // rule applies.
        if i == 1 {
            let authoritative = founder_at(cur.body.issued_at);
            if cur.body.signer_puddle_pubkey != authoritative {
                // For helpful diagnostics, surface the rejected signer
                // prefix. Don't leak the full pubkey to error logs —
                // 8 hex chars is enough to grep for in audit trails.
                let signer_hex = hex::encode(cur.body.signer_puddle_pubkey);
                return Err(ChainError::SignerOutsideFounderWindow {
                    index: i,
                    signer_prefix: signer_hex[..16.min(signer_hex.len())].to_string(),
                });
            }
        } else if cur.body.signer_puddle_pubkey != parent.body.subject_puddle_pubkey {
            return Err(ChainError::SignerNotParentSubject { index: i });
        }

        cur.verify_signature()
            .map_err(|e| ChainError::BadSignature {
                index: i,
                msg: e.to_string(),
            })?;
        expiry_check(cur, i, now)?;
    }
    Ok(())
}

fn expiry_check(token: &Token, index: usize, now: i64) -> std::result::Result<(), ChainError> {
    if let Some(exp) = token.body.expires_at {
        if exp != NEVER_EXPIRES && exp < now {
            return Err(ChainError::Expired {
                index,
                expires_at: exp,
            });
        }
    }
    Ok(())
}

/// Hex-encoding-aware serde for the raw 64-byte signature so the
/// JSON envelope on disk + on the wire is human-readable.
mod serde_signature {
    use super::SIG_LEN;
    use serde::{Deserialize, Deserializer, Serialize, Serializer};

    pub fn serialize<S: Serializer>(bytes: &[u8; SIG_LEN], s: S) -> Result<S::Ok, S::Error> {
        hex::encode(bytes).serialize(s)
    }

    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<[u8; SIG_LEN], D::Error> {
        let s = String::deserialize(d)?;
        let bytes = hex::decode(&s).map_err(serde::de::Error::custom)?;
        if bytes.len() != SIG_LEN {
            return Err(serde::de::Error::custom(format!(
                "signature is {} bytes, expected {}",
                bytes.len(),
                SIG_LEN
            )));
        }
        let mut out = [0u8; SIG_LEN];
        out.copy_from_slice(&bytes);
        Ok(out)
    }
}

/// Convenience: build the founder's bootstrap admin token. Equivalent
/// to constructing the body by hand and calling `Token::issue`, but
/// catches the common foot-shape "I forgot to set parent_token_id =
/// None and signer_puddle_pubkey = founder pubkey."
pub fn issue_bootstrap(
    founder_signing_key: &SigningKey,
    handle: &str,
    issued_at: i64,
    expires_at: Option<i64>,
) -> Result<Token> {
    let founder_pub = founder_signing_key.verifying_key().to_bytes();
    let project_id = project_id_for(&founder_pub, handle);
    let body = TokenBody {
        project_id,
        handle: handle.to_string(),
        subject_puddle_pubkey: founder_pub,
        role: Role::Admin,
        issued_at,
        expires_at,
        signer_puddle_pubkey: founder_pub,
        parent_token_id: None,
    };
    Token::issue(founder_signing_key, body)
        .context("issuing founder bootstrap token")
}

/// Convenience: an admin issues a child token granting `subject_role`
/// to `subject_fleet`. Computes the parent_token_id from `parent` and
/// fills in `signer_puddle_pubkey` from `signing_key`. Returns an error
/// if `parent.body.role != Admin` (only admins can mint children) or
/// if `signing_key` doesn't match `parent.body.subject_puddle_pubkey`
/// (only the parent's grantee can act as the parent).
pub fn issue_child(
    signing_key: &SigningKey,
    parent: &Token,
    subject_fleet: &[u8; ID_LEN],
    subject_role: Role,
    issued_at: i64,
    expires_at: Option<i64>,
) -> Result<Token> {
    if parent.body.role != Role::Admin {
        bail!("parent token is not Admin; only admins can mint children");
    }
    let signer_pub = signing_key.verifying_key().to_bytes();
    if signer_pub != parent.body.subject_puddle_pubkey {
        bail!(
            "signing key {:x?} is not the parent token's subject {:x?}",
            &signer_pub[..4],
            &parent.body.subject_puddle_pubkey[..4]
        );
    }
    let body = TokenBody {
        project_id: parent.body.project_id,
        handle: parent.body.handle.clone(),
        subject_puddle_pubkey: *subject_fleet,
        role: subject_role,
        issued_at,
        expires_at,
        signer_puddle_pubkey: signer_pub,
        parent_token_id: Some(parent.id()),
    };
    Token::issue(signing_key, body).context("issuing child token")
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

    #[test]
    fn project_id_is_sha256_of_founder_pubkey_and_handle() {
        let k = fresh_key();
        let p = project_id_for(&pub_bytes(&k), "vigo");
        let mut hasher = Sha256::new();
        hasher.update(pub_bytes(&k));
        hasher.update(b"vigo");
        let expected: [u8; 32] = hasher.finalize().into();
        assert_eq!(p, expected);
    }

    #[test]
    fn project_id_distinct_per_handle_same_founder() {
        // The whole point of folding the handle into the id: one puddle
        // founds N projects (one per repo), each with its own project_id.
        let k = fresh_key();
        let a = project_id_for(&pub_bytes(&k), "vigo");
        let b = project_id_for(&pub_bytes(&k), "tessera");
        let a2 = project_id_for(&pub_bytes(&k), "vigo");
        assert_ne!(a, b, "different handles must yield different project_ids");
        assert_eq!(a, a2, "same (pubkey, handle) must be stable");
        // Different founders, same handle → still distinct (no squatting).
        let k2 = fresh_key();
        assert_ne!(project_id_for(&pub_bytes(&k2), "vigo"), a);
    }

    #[test]
    fn verify_chain_rejects_forged_project_id_for_someone_elses_pubkey() {
        // Attacker mints a bootstrap claiming a project_id derived from
        // the *victim's* pubkey + a known handle, but signs it with the
        // attacker's own key. `verify_chain` must reject: the bootstrap's
        // signer (attacker) doesn't reproduce the claimed project_id.
        let victim = fresh_key();
        let attacker = fresh_key();
        let victim_project = project_id_for(&pub_bytes(&victim), "secret-repo");
        let body = TokenBody {
            project_id: victim_project,
            handle: "secret-repo".to_string(),
            subject_puddle_pubkey: pub_bytes(&attacker),
            role: Role::Admin,
            issued_at: 100,
            expires_at: None,
            signer_puddle_pubkey: pub_bytes(&attacker),
            parent_token_id: None,
        };
        let forged = Token::issue(&attacker, body).unwrap();
        // Even if the receiver is told the *attacker* is the founder
        // (so the signer matches), the project_id won't reproduce from
        // sha256(attacker ‖ handle), so it's still rejected.
        let r1 = verify_chain(&victim_project, &pub_bytes(&attacker), &[], &[forged.clone()], 200);
        assert_eq!(r1, Err(ChainError::RootNotFounder));
        // And if the receiver is told the victim is the founder, the
        // bootstrap signer (attacker) ≠ founder → rejected too.
        let r2 = verify_chain(&victim_project, &pub_bytes(&victim), &[], &[forged], 200);
        assert_eq!(r2, Err(ChainError::RootNotFounder));
    }

    #[test]
    fn token_id_is_stable_across_calls() {
        let k = fresh_key();
        let token = issue_bootstrap(&k, "proj", 100, None).unwrap();
        let id1 = token.id();
        let id2 = token.id();
        assert_eq!(id1, id2);
    }

    #[test]
    fn token_id_changes_with_any_field_flip() {
        let k = fresh_key();
        let mut a = issue_bootstrap(&k, "proj", 100, None).unwrap();
        let original = a.id();
        // Flip issued_at → ID changes.
        a.body.issued_at = 101;
        assert_ne!(original, a.id());
    }

    #[test]
    fn bootstrap_signature_verifies() {
        let k = fresh_key();
        let token = issue_bootstrap(&k, "proj", 100, None).unwrap();
        token.verify_signature().expect("bootstrap should verify");
    }

    #[test]
    fn tampered_body_fails_verification() {
        let k = fresh_key();
        let mut token = issue_bootstrap(&k, "proj", 100, None).unwrap();
        // Flip a byte in subject_puddle_pubkey.
        token.body.subject_puddle_pubkey[0] ^= 0x01;
        let err = token.verify_signature().unwrap_err();
        assert!(err.to_string().contains("signature verification failed"));
    }

    #[test]
    fn issue_rejects_mismatched_signing_key() {
        let founder = fresh_key();
        let other = fresh_key();
        let body = TokenBody {
            project_id: project_id_for(&pub_bytes(&founder), "proj"),
            handle: "proj".to_string(),
            subject_puddle_pubkey: pub_bytes(&founder),
            role: Role::Admin,
            issued_at: 100,
            expires_at: None,
            signer_puddle_pubkey: pub_bytes(&founder), // claims founder...
            parent_token_id: None,
        };
        // ...but signing with `other` — must reject.
        let err = Token::issue(&other, body).unwrap_err();
        assert!(err.to_string().contains("does not match"));
    }

    #[test]
    fn verify_chain_accepts_lone_bootstrap() {
        let founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        verify_chain(&pid, &pub_bytes(&founder), &[], &[bootstrap], 200).unwrap();
    }

    #[test]
    fn verify_chain_rejects_empty() {
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let r = verify_chain(&pid, &pub_bytes(&founder), &[], &[], 200);
        assert_eq!(r.unwrap_err(), ChainError::Empty);
    }

    #[test]
    fn verify_chain_rejects_root_with_parent_set() {
        let founder = fresh_key();
        let mut bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        bootstrap.body.parent_token_id = Some([0xFF; 32]);
        // Re-sign so signature is over the tampered body — otherwise
        // we'd fail at signature check first.
        let bytes = canonical_bytes(&bootstrap.body);
        bootstrap.signature = founder.sign(&bytes).to_bytes();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let r = verify_chain(&pid, &pub_bytes(&founder), &[], &[bootstrap], 200);
        assert_eq!(r.unwrap_err(), ChainError::RootHasParent);
    }

    #[test]
    fn verify_chain_rejects_root_signed_by_imposter() {
        let founder = fresh_key();
        let imposter = fresh_key();
        // Imposter issues a bootstrap claiming founder's project_id.
        let body = TokenBody {
            project_id: project_id_for(&pub_bytes(&founder), "proj"),
            handle: "proj".to_string(),
            subject_puddle_pubkey: pub_bytes(&imposter),
            role: Role::Admin,
            issued_at: 100,
            expires_at: None,
            signer_puddle_pubkey: pub_bytes(&imposter),
            parent_token_id: None,
        };
        let bootstrap = Token::issue(&imposter, body).unwrap();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let r = verify_chain(&pid, &pub_bytes(&founder), &[], &[bootstrap], 200);
        assert_eq!(r.unwrap_err(), ChainError::RootNotFounder);
    }

    #[test]
    fn verify_chain_two_link_admin_then_member() {
        let founder = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        // Bootstrap is the founder's admin token; founder uses it to
        // mint a member token for `team`.
        let member = issue_child(
            &founder,
            &bootstrap,
            &pub_bytes(&team),
            Role::Member,
            150,
            None,
        )
        .unwrap();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        verify_chain(&pid, &pub_bytes(&founder), &[], &[bootstrap, member], 200).unwrap();
    }

    #[test]
    fn verify_chain_three_link_admin_admin_member() {
        let founder = fresh_key();
        let lieutenant = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let lieutenant_admin = issue_child(
            &founder,
            &bootstrap,
            &pub_bytes(&lieutenant),
            Role::Admin,
            120,
            None,
        )
        .unwrap();
        let team_member = issue_child(
            &lieutenant,
            &lieutenant_admin,
            &pub_bytes(&team),
            Role::Member,
            150,
            None,
        )
        .unwrap();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        verify_chain(
            &pid,
            &pub_bytes(&founder),
            &[],
            &[bootstrap, lieutenant_admin, team_member],
            200,
        )
        .unwrap();
    }

    #[test]
    fn verify_chain_rejects_member_signing_child() {
        let founder = fresh_key();
        let team = fresh_key();
        let other = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        // Founder mints a *member* token for team.
        let team_member = issue_child(
            &founder,
            &bootstrap,
            &pub_bytes(&team),
            Role::Member,
            150,
            None,
        )
        .unwrap();
        // Team tries to mint a child token (members aren't allowed).
        // We construct the body manually because `issue_child` would
        // refuse — we want to test the *verify* path.
        let body = TokenBody {
            project_id: project_id_for(&pub_bytes(&founder), "proj"),
            handle: "proj".to_string(),
            subject_puddle_pubkey: pub_bytes(&other),
            role: Role::Member,
            issued_at: 160,
            expires_at: None,
            signer_puddle_pubkey: pub_bytes(&team),
            parent_token_id: Some(team_member.id()),
        };
        let bogus_child = Token::issue(&team, body).unwrap();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let err = verify_chain(
            &pid,
            &pub_bytes(&founder),
            &[],
            &[bootstrap, team_member, bogus_child],
            200,
        )
        .unwrap_err();
        assert_eq!(err, ChainError::ParentNotAdmin { index: 2 });
    }

    #[test]
    fn verify_chain_rejects_parent_id_mismatch() {
        let founder = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let mut child = issue_child(
            &founder,
            &bootstrap,
            &pub_bytes(&team),
            Role::Member,
            150,
            None,
        )
        .unwrap();
        // Sabotage parent_token_id, then re-sign so signature still
        // matches the body (otherwise the failure would surface as
        // BadSignature).
        child.body.parent_token_id = Some([0xAB; 32]);
        let bytes = canonical_bytes(&child.body);
        child.signature = founder.sign(&bytes).to_bytes();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let err = verify_chain(&pid, &pub_bytes(&founder), &[], &[bootstrap, child], 200)
            .unwrap_err();
        assert_eq!(err, ChainError::ParentMismatch { index: 1 });
    }

    #[test]
    fn verify_chain_rejects_imposter_signing_child_of_bootstrap() {
        // ADR-019: at chain[1], the signer rule is "must be the
        // authoritative founder for this token's issued_at." With no
        // delegations, that's the genesis founder pubkey. An imposter
        // is rejected via SignerOutsideFounderWindow.
        let founder = fresh_key();
        let team = fresh_key();
        let imposter = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let team_member = issue_child(
            &founder,
            &bootstrap,
            &pub_bytes(&team),
            Role::Member,
            150,
            None,
        )
        .unwrap();
        let mut bogus = team_member.clone();
        bogus.body.signer_puddle_pubkey = pub_bytes(&imposter);
        let bytes = canonical_bytes(&bogus.body);
        bogus.signature = imposter.sign(&bytes).to_bytes();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let err = verify_chain(&pid, &pub_bytes(&founder), &[], &[bootstrap, bogus], 200)
            .unwrap_err();
        assert!(
            matches!(err, ChainError::SignerOutsideFounderWindow { index: 1, .. }),
            "expected SignerOutsideFounderWindow, got {:?}",
            err
        );
    }

    #[test]
    fn verify_chain_rejects_signer_not_parent_subject_below_bootstrap() {
        // SignerNotParentSubject still applies at chain depth >= 2
        // (where the parent is an admin token, not the bootstrap).
        // ADR-019's authority-window relaxation is scoped to the
        // bootstrap-to-child link only.
        let founder = fresh_key();
        let lieutenant = fresh_key();
        let team = fresh_key();
        let imposter = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let lieutenant_admin = issue_child(
            &founder,
            &bootstrap,
            &pub_bytes(&lieutenant),
            Role::Admin,
            120,
            None,
        )
        .unwrap();
        // team_member should be signed by lieutenant; instead make it
        // signed by imposter, who isn't lieutenant_admin.subject.
        let team_member = issue_child(
            &lieutenant,
            &lieutenant_admin,
            &pub_bytes(&team),
            Role::Member,
            150,
            None,
        )
        .unwrap();
        let mut bogus = team_member.clone();
        bogus.body.signer_puddle_pubkey = pub_bytes(&imposter);
        let bytes = canonical_bytes(&bogus.body);
        bogus.signature = imposter.sign(&bytes).to_bytes();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let err = verify_chain(
            &pid,
            &pub_bytes(&founder),
            &[],
            &[bootstrap, lieutenant_admin, bogus],
            200,
        )
        .unwrap_err();
        assert_eq!(err, ChainError::SignerNotParentSubject { index: 2 });
    }

    #[test]
    fn verify_chain_rejects_expired_token() {
        let founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, Some(150)).unwrap();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        // now=200, expiry=150 → expired
        let err = verify_chain(&pid, &pub_bytes(&founder), &[], &[bootstrap.clone()], 200)
            .unwrap_err();
        assert_eq!(
            err,
            ChainError::Expired {
                index: 0,
                expires_at: 150
            }
        );
        // now=120, still valid
        verify_chain(&pid, &pub_bytes(&founder), &[], &[bootstrap], 120).unwrap();
    }

    #[test]
    fn verify_chain_rejects_project_id_mismatch_on_inner_token() {
        let founder = fresh_key();
        let team = fresh_key();
        let other_founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let mut child = issue_child(
            &founder,
            &bootstrap,
            &pub_bytes(&team),
            Role::Member,
            150,
            None,
        )
        .unwrap();
        // Swap project_id to a different project, re-sign.
        child.body.project_id = project_id_for(&pub_bytes(&other_founder), "proj");
        let bytes = canonical_bytes(&child.body);
        child.signature = founder.sign(&bytes).to_bytes();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let err =
            verify_chain(&pid, &pub_bytes(&founder), &[], &[bootstrap, child], 200).unwrap_err();
        assert_eq!(err, ChainError::ProjectIdMismatch { index: 1 });
    }

    #[test]
    fn json_roundtrip_preserves_signature_and_id() {
        let founder = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let member = issue_child(
            &founder,
            &bootstrap,
            &pub_bytes(&team),
            Role::Member,
            150,
            Some(9_999_999_999),
        )
        .unwrap();
        let id_before = member.id();
        let s = serde_json::to_string(&member).unwrap();
        let parsed: Token = serde_json::from_str(&s).unwrap();
        assert_eq!(parsed.id(), id_before);
        parsed.verify_signature().unwrap();
    }

    #[test]
    fn role_byte_roundtrip() {
        for role in [Role::Admin, Role::Member] {
            let b = role.as_byte();
            let r2 = Role::from_byte(b).unwrap();
            assert_eq!(role, r2);
        }
        assert!(Role::from_byte(0xFF).is_err());
    }

    #[test]
    fn canonical_bytes_are_byte_stable() {
        // Two structurally-identical bodies must produce identical
        // canonical bytes — that's the whole point of the hand-rolled
        // encoding. Construct two bodies field-by-field with values
        // chosen to differ only in field representation (Option::Some(0)
        // vs Option::None for expires_at, when 0 is the wire sentinel).
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let body_none = TokenBody {
            project_id: pid,
            handle: "proj".to_string(),
            subject_puddle_pubkey: pub_bytes(&founder),
            role: Role::Admin,
            issued_at: 100,
            expires_at: None,
            signer_puddle_pubkey: pub_bytes(&founder),
            parent_token_id: None,
        };
        let body_zero = TokenBody {
            expires_at: Some(0),
            ..body_none.clone()
        };
        // Sentinel encoding: None and Some(0) both render as 8 zero
        // bytes in the canonical form.
        assert_eq!(canonical_bytes(&body_none), canonical_bytes(&body_zero));
    }

    // ---- ADR-019 chained verification ----

    use super::super::envelopes::{DelegationEnvelope, DELEGATION_MAX_LEN};
    use super::super::project_state::hex_id;

    /// Issue a fresh admin token at chain[1] signed by `signer_key`,
    /// parented to `bootstrap`. Used to simulate post-rekey admin
    /// minting for the chained-verification tests.
    fn issue_admin_under_signer(
        signer_key: &SigningKey,
        bootstrap: &Token,
        subject: &[u8; ID_LEN],
        issued_at: i64,
    ) -> Token {
        let body = TokenBody {
            project_id: bootstrap.body.project_id,
            handle: "proj".to_string(),
            subject_puddle_pubkey: *subject,
            role: Role::Admin,
            issued_at,
            expires_at: None,
            signer_puddle_pubkey: pub_bytes(signer_key),
            parent_token_id: Some(bootstrap.id()),
        };
        Token::issue(signer_key, body).expect("issue admin under signer")
    }

    #[test]
    fn verify_chain_accepts_post_rekey_admin_via_single_delegation() {
        // Personal-DR / single-rekey scenario: founder rekeys, mints
        // a fresh admin token under the new key. Chain is bootstrap
        // (signed by genesis) → admin (signed by NEW). Delegation
        // chain bridges genesis → NEW.
        let genesis = fresh_key();
        let new_founder = fresh_key();
        let new_admin = fresh_key(); // any subject, distinct from signer

        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        // Delegation: signed by genesis, names new_founder, dated AFTER bootstrap.
        let d0 = DelegationEnvelope::build(
            &genesis,
            &project_id_for(&pub_bytes(&genesis), "proj"),
            &pub_bytes(&new_founder),
            200,
        );
        // Admin minted POST-delegation (issued_at >= delegated_at = 200).
        let admin = issue_admin_under_signer(
            &new_founder,
            &bootstrap,
            &pub_bytes(&new_admin),
            300,
        );
        let pid = project_id_for(&pub_bytes(&genesis), "proj");
        verify_chain(&pid, &pub_bytes(&genesis), &[d0], &[bootstrap, admin], 400)
            .expect("post-rekey admin should verify via delegation");
    }

    #[test]
    fn verify_chain_accepts_pre_rekey_admin_after_delegation_appended() {
        // Tokens issued BEFORE the rekey must still verify after the
        // delegation appears on disk. The genesis founder's window
        // covers any timestamp before delegations[0].delegated_at.
        let genesis = fresh_key();
        let new_founder = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let pre_rekey_admin = issue_child(
            &genesis,
            &bootstrap,
            &pub_bytes(&team),
            Role::Admin,
            150, // before delegation at 200
            None,
        )
        .unwrap();
        let d0 = DelegationEnvelope::build(
            &genesis,
            &project_id_for(&pub_bytes(&genesis), "proj"),
            &pub_bytes(&new_founder),
            200,
        );
        let pid = project_id_for(&pub_bytes(&genesis), "proj");
        verify_chain(
            &pid,
            &pub_bytes(&genesis),
            &[d0],
            &[bootstrap, pre_rekey_admin],
            400,
        )
        .expect("pre-rekey admin should still verify after delegation appended");
    }

    #[test]
    fn verify_chain_accepts_three_chained_delegations() {
        // Three sequential rekeys: genesis → r1 → r2 → r3. Admin
        // minted under r3 verifies through the chained delegations.
        let genesis = fresh_key();
        let r1 = fresh_key();
        let r2 = fresh_key();
        let r3 = fresh_key();
        let admin_subject = fresh_key();
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&genesis), "proj");
        let d0 = DelegationEnvelope::build(&genesis, &pid, &pub_bytes(&r1), 200);
        let d1 = DelegationEnvelope::build(&r1, &pid, &pub_bytes(&r2), 300);
        let d2 = DelegationEnvelope::build(&r2, &pid, &pub_bytes(&r3), 400);
        let admin = issue_admin_under_signer(&r3, &bootstrap, &pub_bytes(&admin_subject), 500);
        verify_chain(
            &pid,
            &pub_bytes(&genesis),
            &[d0, d1, d2],
            &[bootstrap, admin],
            600,
        )
        .expect("three-rekey chain should verify");
    }

    #[test]
    fn verify_chain_rejects_deposed_founder_signing_outside_window() {
        // ADR-019 deposed-founder attack: r1 was a legitimate founder,
        // then was retired by d_1 in favor of r2. r1's compromised
        // key is used to sign a fresh admin token dated AFTER d_1.
        // Verification rejects because r1's window closed at d_1.
        let genesis = fresh_key();
        let r1 = fresh_key();
        let r2 = fresh_key();
        let admin_subject = fresh_key();
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&genesis), "proj");
        let d0 = DelegationEnvelope::build(&genesis, &pid, &pub_bytes(&r1), 200);
        let d1 = DelegationEnvelope::build(&r1, &pid, &pub_bytes(&r2), 300);
        // Attacker signs a fresh admin under r1 with issued_at past d_1.
        let attacker_admin =
            issue_admin_under_signer(&r1, &bootstrap, &pub_bytes(&admin_subject), 500);
        let err = verify_chain(
            &pid,
            &pub_bytes(&genesis),
            &[d0, d1],
            &[bootstrap, attacker_admin],
            600,
        )
        .unwrap_err();
        assert!(
            matches!(err, ChainError::SignerOutsideFounderWindow { index: 1, .. }),
            "expected SignerOutsideFounderWindow, got {:?}",
            err
        );
    }

    #[test]
    fn verify_chain_rejects_token_predating_its_signer_window() {
        // Inverse of the deposed-founder case: r1 cannot sign tokens
        // dated BEFORE d_0 (which is when r1's window opens).
        let genesis = fresh_key();
        let r1 = fresh_key();
        let admin_subject = fresh_key();
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&genesis), "proj");
        let d0 = DelegationEnvelope::build(&genesis, &pid, &pub_bytes(&r1), 200);
        // r1 attempts to sign a token at issued_at=150 (before d_0
        // at 200). Outside r1's window, inside genesis's.
        let antedated =
            issue_admin_under_signer(&r1, &bootstrap, &pub_bytes(&admin_subject), 150);
        let err = verify_chain(
            &pid,
            &pub_bytes(&genesis),
            &[d0],
            &[bootstrap, antedated],
            300,
        )
        .unwrap_err();
        assert!(
            matches!(err, ChainError::SignerOutsideFounderWindow { index: 1, .. }),
            "expected SignerOutsideFounderWindow for pre-window token, got {:?}",
            err
        );
    }

    #[test]
    fn verify_chain_rejects_forged_delegation_signed_by_attacker() {
        // Attacker forges a delegation envelope claiming OLD signed
        // it, but actually signs with their own key. Signature
        // verification on the delegation rejects.
        let genesis = fresh_key();
        let attacker = fresh_key();
        let attacker_pubkey = pub_bytes(&attacker);
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&genesis), "proj");
        // Attacker signs the delegation with their own key but claims
        // genesis is the signer.
        let mut forged = DelegationEnvelope::build(&attacker, &pid, &attacker_pubkey, 200);
        forged.old_founder_pubkey = hex_id(&pub_bytes(&genesis));
        // Don't re-sign — the signature is over the original
        // (attacker, attacker) bytes; substituting old_founder_pubkey
        // will fail verification.
        let pid_for_verify = pid;
        let err = verify_chain(
            &pid_for_verify,
            &pub_bytes(&genesis),
            &[forged],
            &[bootstrap],
            400,
        )
        .unwrap_err();
        // Will surface as either DelegationBadSignature (signature
        // doesn't verify against the claimed pubkey) or
        // DelegationLinkBroken (depending on which check fires
        // first); both are correct rejections.
        assert!(
            matches!(
                err,
                ChainError::DelegationBadSignature { index: 0, .. }
                    | ChainError::DelegationLinkBroken { index: 0 }
            ),
            "expected delegation-rejection variant, got {:?}",
            err
        );
    }

    #[test]
    fn verify_chain_rejects_broken_delegation_link() {
        // delegations[1].old_founder_pubkey doesn't equal
        // delegations[0].new_founder_pubkey — chain integrity broken.
        let genesis = fresh_key();
        let r1 = fresh_key();
        let imposter = fresh_key();
        let r2 = fresh_key();
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&genesis), "proj");
        let d0 = DelegationEnvelope::build(&genesis, &pid, &pub_bytes(&r1), 200);
        // d_1 is signed by `imposter`, not r1 — link is broken.
        let d1_bad = DelegationEnvelope::build(&imposter, &pid, &pub_bytes(&r2), 300);
        let err =
            verify_chain(&pid, &pub_bytes(&genesis), &[d0, d1_bad], &[bootstrap], 400)
                .unwrap_err();
        assert_eq!(err, ChainError::DelegationLinkBroken { index: 1 });
    }

    #[test]
    fn verify_chain_rejects_out_of_order_delegations() {
        // delegated_at must be strictly increasing — closes a subtle
        // attack where a forged envelope is squeezed in at the same
        // instant as a legitimate one.
        let genesis = fresh_key();
        let r1 = fresh_key();
        let r2 = fresh_key();
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&genesis), "proj");
        // Identical delegated_at on both → ordering violated.
        let d0 = DelegationEnvelope::build(&genesis, &pid, &pub_bytes(&r1), 200);
        let d1 = DelegationEnvelope::build(&r1, &pid, &pub_bytes(&r2), 200);
        let err =
            verify_chain(&pid, &pub_bytes(&genesis), &[d0, d1], &[bootstrap], 400)
                .unwrap_err();
        assert_eq!(err, ChainError::DelegationOrderBroken { index: 1 });
    }

    #[test]
    fn verify_chain_rejects_delegation_with_wrong_project_id() {
        // A delegation crafted for a *different* project must be
        // rejected — closes a "cross-project replay" path.
        let genesis = fresh_key();
        let other_genesis = fresh_key();
        let r1 = fresh_key();
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let our_pid = project_id_for(&pub_bytes(&genesis), "proj");
        let other_pid = project_id_for(&pub_bytes(&other_genesis), "proj");
        // Delegation signed by genesis but pointed at some other
        // project. Signature verifies against the canonical bytes
        // including the other_pid; we need to manually construct it
        // so the project_id field disagrees with the chain anchor.
        let d_wrong_pid =
            DelegationEnvelope::build(&genesis, &other_pid, &pub_bytes(&r1), 200);
        let err = verify_chain(
            &our_pid,
            &pub_bytes(&genesis),
            &[d_wrong_pid],
            &[bootstrap],
            400,
        )
        .unwrap_err();
        assert_eq!(err, ChainError::DelegationProjectIdMismatch { index: 0 });
    }

    #[test]
    fn verify_chain_rejects_delegation_chain_over_cap() {
        // Hard cap belt + braces: even if a peer presents a chain
        // longer than DELEGATION_MAX_LEN, the verifier rejects it
        // before walking signatures.
        let genesis = fresh_key();
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&genesis), "proj");
        let mut delegations = Vec::with_capacity(DELEGATION_MAX_LEN + 1);
        let mut prev_signer = genesis.clone();
        for i in 0..(DELEGATION_MAX_LEN + 1) {
            let next = fresh_key();
            delegations.push(DelegationEnvelope::build(
                &prev_signer,
                &pid,
                &pub_bytes(&next),
                200 + i as i64,
            ));
            prev_signer = next;
        }
        let err = verify_chain(
            &pid,
            &pub_bytes(&genesis),
            &delegations,
            &[bootstrap],
            1000,
        )
        .unwrap_err();
        assert_eq!(
            err,
            ChainError::DelegationCapExceeded {
                len: DELEGATION_MAX_LEN + 1,
            }
        );
    }

    #[test]
    fn verify_chain_pre_rekey_chain_unaffected_by_empty_delegation_list() {
        // Identity check: passing an empty delegation list reproduces
        // the pre-ADR-019 behavior exactly. No regression for projects
        // that haven't yet seen a founder rekey.
        let founder = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let member = issue_child(
            &founder,
            &bootstrap,
            &pub_bytes(&team),
            Role::Member,
            150,
            None,
        )
        .unwrap();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        verify_chain(&pid, &pub_bytes(&founder), &[], &[bootstrap, member], 200)
            .expect("empty delegations must reproduce pre-ADR-019 behavior");
    }
}
