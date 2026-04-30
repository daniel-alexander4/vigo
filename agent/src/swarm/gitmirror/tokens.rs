// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Capability tokens for unified-substrate gitmirror (ADR-015).
//!
//! Membership in a project is a stack of Ed25519-signed capability
//! tokens, anchored to the founder's fleet pubkey. The receiving
//! envoy verifies an incoming push by walking the chain of tokens
//! the caller presents back to the project's anchor:
//!
//! ```text
//!  founder fleet F0  ──signs──▶  bootstrap admin token T0 (subject = F0)
//!                                             │
//!                                             │ admin T0 signs ▼
//!                                  member token T1 (subject = F1, role = member)
//! ```
//!
//! The token bytes signed by the issuer are a hand-rolled
//! deterministic encoding (matching cryptsync's leaves.rs pattern,
//! never JSON whitespace). Token ID is the sha256 of those exact
//! signed bytes — content-addressed, so revocation envelopes can name
//! a token by id without ambiguity.
//!
//! Phase 1 of ADR-015 (this module) ships the cryptographic
//! primitives: token shape, canonical encoding, signing,
//! single-token + chain verification. Persistence (`~/.vigo-gitmirror/
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
pub const TOKEN_DOMAIN: &[u8] = b"vigo-gitmirror-token-v1\0";

/// Length of fleet pubkeys + token IDs (raw Ed25519 pubkey + sha256).
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
/// Field order in the canonical bytes (1 byte version-prefix-included
/// via TOKEN_DOMAIN, then):
/// 1. project_id (32B)
/// 2. subject_fleet_pubkey (32B)
/// 3. role (1B)
/// 4. issued_at (8B BE i64)
/// 5. expires_at (8B BE i64, 0 = NEVER_EXPIRES)
/// 6. signer_fleet_pubkey (32B)
/// 7. parent_token_id (32B, NO_PARENT when None)
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TokenBody {
    /// Project anchor. `project_id = sha256(founder_fleet_pubkey)`
    /// — content-addressed so the URL `vigo://<project_id>/<handle>`
    /// can't be squatted.
    pub project_id: [u8; ID_LEN],
    /// Fleet pubkey this token grants access to. Holders of fleet
    /// F's wrapped key (any envoy that ran `self-fleet unlock` /
    /// `join`) can push under a token whose subject is F.
    pub subject_fleet_pubkey: [u8; ID_LEN],
    /// Role granted to the subject fleet.
    pub role: Role,
    /// Unix seconds when this token was issued.
    pub issued_at: i64,
    /// Unix seconds when this token stops being valid. `None`
    /// (encoded as `0`) means no expiry — the founder's bootstrap
    /// token typically has no expiry; member tokens often do.
    pub expires_at: Option<i64>,
    /// Pubkey of the fleet that signed this token. For the
    /// founder's bootstrap token, this equals the founder pubkey
    /// (= sha256-input of project_id). For derived tokens, this
    /// is the admin's subject_fleet_pubkey.
    pub signer_fleet_pubkey: [u8; ID_LEN],
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
    let mut out = Vec::with_capacity(TOKEN_DOMAIN.len() + 32 + 32 + 1 + 8 + 8 + 32 + 32);
    out.extend_from_slice(TOKEN_DOMAIN);
    out.extend_from_slice(&body.project_id);
    out.extend_from_slice(&body.subject_fleet_pubkey);
    out.push(body.role.as_byte());
    out.extend_from_slice(&body.issued_at.to_be_bytes());
    out.extend_from_slice(&body.expires_at.unwrap_or(NEVER_EXPIRES).to_be_bytes());
    out.extend_from_slice(&body.signer_fleet_pubkey);
    out.extend_from_slice(&body.parent_token_id.unwrap_or(NO_PARENT));
    out
}

/// `project_id = sha256(founder_fleet_pubkey)`. Stable across all
/// envoys; baked into the URL.
pub fn project_id_for(founder_fleet_pubkey: &[u8; ID_LEN]) -> [u8; ID_LEN] {
    let mut hasher = Sha256::new();
    hasher.update(founder_fleet_pubkey);
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
    /// Ed25519 private key the issuing fleet holds) and the body.
    /// The body's `signer_fleet_pubkey` MUST equal the public half of
    /// `signing_key`; we re-check here so a misuse fails loudly
    /// instead of producing a body that won't verify.
    pub fn issue(signing_key: &SigningKey, body: TokenBody) -> Result<Token> {
        let derived_pub = signing_key.verifying_key().to_bytes();
        if derived_pub != body.signer_fleet_pubkey {
            bail!(
                "signing key does not match body.signer_fleet_pubkey \
                 (key {:x?}, body {:x?})",
                &derived_pub[..4],
                &body.signer_fleet_pubkey[..4]
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
    /// `signer_fleet_pubkey`. Does NOT verify the chain — call
    /// [`verify_chain`] for full anchor-rooted verification.
    pub fn verify_signature(&self) -> Result<()> {
        let bytes = canonical_bytes(&self.body);
        let vk = VerifyingKey::from_bytes(&self.body.signer_fleet_pubkey)
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
    /// The first token's signer_fleet_pubkey didn't match the project's
    /// founder pubkey (project_id mismatch caught earlier; this catches
    /// a token signed by someone other than the founder claiming to be
    /// a root).
    RootNotFounder,
    /// A non-root token had its parent_token_id set to a value that
    /// didn't match the previous token in the chain.
    ParentMismatch { index: usize },
    /// A non-root token's signer_fleet_pubkey didn't match the
    /// subject_fleet_pubkey of the parent token (whoever signed this
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
                "root token's signer is not the project's founder fleet pubkey"
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
        }
    }
}

impl std::error::Error for ChainError {}

/// Verify a token chain against the project's founder pubkey at a
/// given `now` (unix seconds — pass `now_secs()` in production; tests
/// inject a fixed value).
///
/// Chain layout: `chain[0]` is the founder's bootstrap token (parent
/// = None, signer = founder_fleet_pubkey). Each subsequent
/// `chain[i]` was signed by `chain[i-1]` (whose role MUST be Admin
/// for it to have minted children). `chain[chain.len()-1]` is the
/// "leaf" — the token actually presented by the caller for
/// authorization.
///
/// On Ok, the leaf's role + subject_fleet_pubkey are the
/// authorization context the caller is asserting.
pub fn verify_chain(
    project_id: &[u8; ID_LEN],
    founder_fleet_pubkey: &[u8; ID_LEN],
    chain: &[Token],
    now: i64,
) -> std::result::Result<(), ChainError> {
    if chain.is_empty() {
        return Err(ChainError::Empty);
    }

    // Check the project_id is consistent with the founder pubkey
    // (catches "different project entirely" before signature work).
    let expected_project_id = project_id_for(founder_fleet_pubkey);
    if &expected_project_id != project_id {
        return Err(ChainError::RootNotFounder);
    }

    // Root token must be the founder's bootstrap token.
    let root = &chain[0];
    if root.body.parent_token_id.is_some() {
        return Err(ChainError::RootHasParent);
    }
    if &root.body.signer_fleet_pubkey != founder_fleet_pubkey {
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

    // Walk the rest of the chain.
    for i in 1..chain.len() {
        let parent = &chain[i - 1];
        let cur = &chain[i];

        if &cur.body.project_id != project_id {
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
        if cur.body.signer_fleet_pubkey != parent.body.subject_fleet_pubkey {
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
/// None and signer_fleet_pubkey = founder pubkey."
pub fn issue_bootstrap(
    founder_signing_key: &SigningKey,
    issued_at: i64,
    expires_at: Option<i64>,
) -> Result<Token> {
    let founder_pub = founder_signing_key.verifying_key().to_bytes();
    let project_id = project_id_for(&founder_pub);
    let body = TokenBody {
        project_id,
        subject_fleet_pubkey: founder_pub,
        role: Role::Admin,
        issued_at,
        expires_at,
        signer_fleet_pubkey: founder_pub,
        parent_token_id: None,
    };
    Token::issue(founder_signing_key, body)
        .context("issuing founder bootstrap token")
}

/// Convenience: an admin issues a child token granting `subject_role`
/// to `subject_fleet`. Computes the parent_token_id from `parent` and
/// fills in `signer_fleet_pubkey` from `signing_key`. Returns an error
/// if `parent.body.role != Admin` (only admins can mint children) or
/// if `signing_key` doesn't match `parent.body.subject_fleet_pubkey`
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
    if signer_pub != parent.body.subject_fleet_pubkey {
        bail!(
            "signing key {:x?} is not the parent token's subject {:x?}",
            &signer_pub[..4],
            &parent.body.subject_fleet_pubkey[..4]
        );
    }
    let body = TokenBody {
        project_id: parent.body.project_id,
        subject_fleet_pubkey: *subject_fleet,
        role: subject_role,
        issued_at,
        expires_at,
        signer_fleet_pubkey: signer_pub,
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
    fn project_id_is_sha256_of_founder_pubkey() {
        let k = fresh_key();
        let p = project_id_for(&pub_bytes(&k));
        let mut hasher = Sha256::new();
        hasher.update(pub_bytes(&k));
        let expected: [u8; 32] = hasher.finalize().into();
        assert_eq!(p, expected);
    }

    #[test]
    fn token_id_is_stable_across_calls() {
        let k = fresh_key();
        let token = issue_bootstrap(&k, 100, None).unwrap();
        let id1 = token.id();
        let id2 = token.id();
        assert_eq!(id1, id2);
    }

    #[test]
    fn token_id_changes_with_any_field_flip() {
        let k = fresh_key();
        let mut a = issue_bootstrap(&k, 100, None).unwrap();
        let original = a.id();
        // Flip issued_at → ID changes.
        a.body.issued_at = 101;
        assert_ne!(original, a.id());
    }

    #[test]
    fn bootstrap_signature_verifies() {
        let k = fresh_key();
        let token = issue_bootstrap(&k, 100, None).unwrap();
        token.verify_signature().expect("bootstrap should verify");
    }

    #[test]
    fn tampered_body_fails_verification() {
        let k = fresh_key();
        let mut token = issue_bootstrap(&k, 100, None).unwrap();
        // Flip a byte in subject_fleet_pubkey.
        token.body.subject_fleet_pubkey[0] ^= 0x01;
        let err = token.verify_signature().unwrap_err();
        assert!(err.to_string().contains("signature verification failed"));
    }

    #[test]
    fn issue_rejects_mismatched_signing_key() {
        let founder = fresh_key();
        let other = fresh_key();
        let body = TokenBody {
            project_id: project_id_for(&pub_bytes(&founder)),
            subject_fleet_pubkey: pub_bytes(&founder),
            role: Role::Admin,
            issued_at: 100,
            expires_at: None,
            signer_fleet_pubkey: pub_bytes(&founder), // claims founder...
            parent_token_id: None,
        };
        // ...but signing with `other` — must reject.
        let err = Token::issue(&other, body).unwrap_err();
        assert!(err.to_string().contains("does not match"));
    }

    #[test]
    fn verify_chain_accepts_lone_bootstrap() {
        let founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&founder));
        verify_chain(&pid, &pub_bytes(&founder), &[bootstrap], 200).unwrap();
    }

    #[test]
    fn verify_chain_rejects_empty() {
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder));
        let r = verify_chain(&pid, &pub_bytes(&founder), &[], 200);
        assert_eq!(r.unwrap_err(), ChainError::Empty);
    }

    #[test]
    fn verify_chain_rejects_root_with_parent_set() {
        let founder = fresh_key();
        let mut bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
        bootstrap.body.parent_token_id = Some([0xFF; 32]);
        // Re-sign so signature is over the tampered body — otherwise
        // we'd fail at signature check first.
        let bytes = canonical_bytes(&bootstrap.body);
        bootstrap.signature = founder.sign(&bytes).to_bytes();
        let pid = project_id_for(&pub_bytes(&founder));
        let r = verify_chain(&pid, &pub_bytes(&founder), &[bootstrap], 200);
        assert_eq!(r.unwrap_err(), ChainError::RootHasParent);
    }

    #[test]
    fn verify_chain_rejects_root_signed_by_imposter() {
        let founder = fresh_key();
        let imposter = fresh_key();
        // Imposter issues a bootstrap claiming founder's project_id.
        let body = TokenBody {
            project_id: project_id_for(&pub_bytes(&founder)),
            subject_fleet_pubkey: pub_bytes(&imposter),
            role: Role::Admin,
            issued_at: 100,
            expires_at: None,
            signer_fleet_pubkey: pub_bytes(&imposter),
            parent_token_id: None,
        };
        let bootstrap = Token::issue(&imposter, body).unwrap();
        let pid = project_id_for(&pub_bytes(&founder));
        let r = verify_chain(&pid, &pub_bytes(&founder), &[bootstrap], 200);
        assert_eq!(r.unwrap_err(), ChainError::RootNotFounder);
    }

    #[test]
    fn verify_chain_two_link_admin_then_member() {
        let founder = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
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
        let pid = project_id_for(&pub_bytes(&founder));
        verify_chain(&pid, &pub_bytes(&founder), &[bootstrap, member], 200).unwrap();
    }

    #[test]
    fn verify_chain_three_link_admin_admin_member() {
        let founder = fresh_key();
        let lieutenant = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
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
        let pid = project_id_for(&pub_bytes(&founder));
        verify_chain(
            &pid,
            &pub_bytes(&founder),
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
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
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
            project_id: project_id_for(&pub_bytes(&founder)),
            subject_fleet_pubkey: pub_bytes(&other),
            role: Role::Member,
            issued_at: 160,
            expires_at: None,
            signer_fleet_pubkey: pub_bytes(&team),
            parent_token_id: Some(team_member.id()),
        };
        let bogus_child = Token::issue(&team, body).unwrap();
        let pid = project_id_for(&pub_bytes(&founder));
        let err = verify_chain(
            &pid,
            &pub_bytes(&founder),
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
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
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
        let pid = project_id_for(&pub_bytes(&founder));
        let err = verify_chain(&pid, &pub_bytes(&founder), &[bootstrap, child], 200)
            .unwrap_err();
        assert_eq!(err, ChainError::ParentMismatch { index: 1 });
    }

    #[test]
    fn verify_chain_rejects_signer_not_parent_subject() {
        let founder = fresh_key();
        let team = fresh_key();
        let imposter = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
        // `team_member` was signed by founder. If we replace its
        // signer_fleet_pubkey with imposter's and re-sign with imposter,
        // the chain check should reject because imposter isn't the
        // parent's subject (parent is bootstrap, subject = founder).
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
        bogus.body.signer_fleet_pubkey = pub_bytes(&imposter);
        let bytes = canonical_bytes(&bogus.body);
        bogus.signature = imposter.sign(&bytes).to_bytes();
        let pid = project_id_for(&pub_bytes(&founder));
        let err = verify_chain(&pid, &pub_bytes(&founder), &[bootstrap, bogus], 200)
            .unwrap_err();
        assert_eq!(err, ChainError::SignerNotParentSubject { index: 1 });
    }

    #[test]
    fn verify_chain_rejects_expired_token() {
        let founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, Some(150)).unwrap();
        let pid = project_id_for(&pub_bytes(&founder));
        // now=200, expiry=150 → expired
        let err = verify_chain(&pid, &pub_bytes(&founder), &[bootstrap.clone()], 200)
            .unwrap_err();
        assert_eq!(
            err,
            ChainError::Expired {
                index: 0,
                expires_at: 150
            }
        );
        // now=120, still valid
        verify_chain(&pid, &pub_bytes(&founder), &[bootstrap], 120).unwrap();
    }

    #[test]
    fn verify_chain_rejects_project_id_mismatch_on_inner_token() {
        let founder = fresh_key();
        let team = fresh_key();
        let other_founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
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
        child.body.project_id = project_id_for(&pub_bytes(&other_founder));
        let bytes = canonical_bytes(&child.body);
        child.signature = founder.sign(&bytes).to_bytes();
        let pid = project_id_for(&pub_bytes(&founder));
        let err =
            verify_chain(&pid, &pub_bytes(&founder), &[bootstrap, child], 200).unwrap_err();
        assert_eq!(err, ChainError::ProjectIdMismatch { index: 1 });
    }

    #[test]
    fn json_roundtrip_preserves_signature_and_id() {
        let founder = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
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
        let pid = project_id_for(&pub_bytes(&founder));
        let body_none = TokenBody {
            project_id: pid,
            subject_fleet_pubkey: pub_bytes(&founder),
            role: Role::Admin,
            issued_at: 100,
            expires_at: None,
            signer_fleet_pubkey: pub_bytes(&founder),
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
}
