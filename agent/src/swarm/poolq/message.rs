// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Signed message envelope for poolq (ADR-029).
//!
//! A poolq message is a small, immutable, signed JSON envelope. Founder-only
//! posting in v1: the `author_puddle_pubkey` is also the topic founder, so
//! `topic_id = sha256(author_pubkey ‖ topic_name)` self-anchors the message
//! to its topic. The mesh and every consumer verify the signature end-to-end
//! against `author_pubkey`; multi-writer delegation (ADR-019 chain) is a v2
//! lever.
//!
//! ### Why hand-rolled canonical bytes
//!
//! The same reason every other Vigo signed envelope (`anchor/tokens.rs`,
//! `curator/catalog.rs`, `puddle/name_claim.rs`) hand-rolls its signed bytes:
//! `serde_json` is non-deterministic across whitespace, field order, and
//! integer-representation choices, so two encodings of the same body can
//! produce different bytes — and therefore different signatures — for what
//! looks like the same message. The signed shape is fixed; the JSON form on
//! disk is for storage only and is re-encoded into canonical bytes whenever
//! a signature is created or verified.
//!
//! ### Domain separator
//!
//! [`POOLQ_MSG_DOMAIN`] = `b"poolq-msg-v1\0"`. Distinct from every other
//! Vigo signed shape — `curator-catalog-v2\0`, `gitback-token-v2\0`,
//! `gitback-delegate-v2\0`, and the puddle name-claim domains — so a
//! signature crafted under one domain cannot be replayed under another even
//! with matching key material. Bumped if the message shape ever changes
//! (existing entries would then fail verification and be dropped, never
//! silently reinterpreted under the new shape — the curator-`v1`→`v2`
//! `kind`-discriminator precedent).

use anyhow::{anyhow, bail, Context, Result};
use ed25519_dalek::{Signer, SigningKey, Verifier, VerifyingKey};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

/// Domain-separator prefix for the canonical bytes a poolq message signs.
/// See module docs for the cross-subsystem rationale.
pub const POOLQ_MSG_DOMAIN: &[u8] = b"poolq-msg-v1\0";

/// `schema_version` stamped on every freshly-signed [`MessageBody`]. Bumped
/// only when the canonical-bytes layout changes; encoded into the signed
/// bytes so a pre-`v2` envelope fails verification under a v2 reader rather
/// than being mis-read.
pub const SCHEMA_VERSION: u32 = 1;

/// Length of a raw Ed25519 puddle pubkey / a sha256 (topic_id, msg_id).
pub const ID_LEN: usize = 32;

/// Length of an Ed25519 signature.
pub const SIG_LEN: usize = 64;

/// Max topic-name length. Matches gitback project-handle / curator artifact-
/// name / puddle-name so `poolq://<puddle-name>/<topic_name>` references stay
/// bounded.
pub const MAX_TOPIC_NAME_LEN: usize = 64;

/// Hard ceiling on per-message body bytes, enforced at the publish call
/// site regardless of what the operator sets `swarm.poolq.max_msg_bytes`
/// to. The operator's `max_msg_bytes` tunes inside `[0, HARD_MAX_MSG_BYTES]`
/// — bigger payloads belong in a curator artifact + a poolq message that
/// references it by id.
pub const HARD_MAX_MSG_BYTES: usize = 64 * 1024;

/// Validate a topic name at publish time. Same character rules as gitback
/// project handles / curator artifact names / puddle names: 1-64 chars,
/// ASCII alphanumeric + `-` `_` `.`, no leading dot, not 64 lowercase hex
/// (which would be ambiguous with the hex `topic_id` reference form
/// `poolq://<topic_id_hex>/<topic_name>`).
pub fn validate_topic_name(name: &str) -> Result<()> {
    if name.is_empty() || name.len() > MAX_TOPIC_NAME_LEN {
        bail!("topic name must be 1-{} characters", MAX_TOPIC_NAME_LEN);
    }
    if name.starts_with('.') {
        bail!("topic name must not start with '.'");
    }
    if name.len() == 64 && name.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)) {
        bail!("topic name must not look like a topic_id (64 lowercase hex characters)");
    }
    for c in name.chars() {
        if !(c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.') {
            bail!(
                "topic name '{}' contains invalid character '{}'; use ASCII alphanumeric + '-' '_' '.'",
                name,
                c
            );
        }
    }
    Ok(())
}

/// `topic_id = sha256(founder_puddle_pubkey ‖ topic_name)` — frozen across
/// envoys; baked into the URL `poolq://<topic_id>/<topic_name>` (and the
/// friendly `poolq://<puddle-name>/<topic_name>` via the ADR-022 name map).
/// Same shape as [`crate::swarm::anchor::tokens::project_id_for`] and
/// `curator::catalog::artifact_id_for`.
pub fn topic_id_for(founder_puddle_pubkey: &[u8; ID_LEN], topic_name: &str) -> [u8; ID_LEN] {
    let mut hasher = Sha256::new();
    hasher.update(founder_puddle_pubkey);
    hasher.update(topic_name.as_bytes());
    let digest = hasher.finalize();
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&digest);
    out
}

/// `msg_id = sha256(canonical_bytes)` — content-addressed message id. Stable;
/// independent of the signature. Two byte-identical messages share the same
/// id, which is the G-Set CRDT's merge key.
pub fn msg_id_for(canonical: &[u8]) -> [u8; ID_LEN] {
    let mut hasher = Sha256::new();
    hasher.update(canonical);
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

fn parse_hex64(s: &str, what: &str) -> Result<[u8; SIG_LEN]> {
    let bytes = hex::decode(s).with_context(|| format!("{what}: invalid hex"))?;
    if bytes.len() != SIG_LEN {
        bail!("{what}: {} bytes, expected {}", bytes.len(), SIG_LEN);
    }
    let mut out = [0u8; SIG_LEN];
    out.copy_from_slice(&bytes);
    Ok(out)
}

/// The signed body of a poolq message. Field order in canonical bytes is
/// fixed (see [`canonical_bytes`]); the JSON `serde` shape is for storage
/// only and is re-encoded canonically whenever a signature is created or
/// verified.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct MessageBody {
    /// Carries the v1 → v2 → ... layout discriminator. A reader at a higher
    /// version verifies its known layout; a pre-v2 envelope's bytes
    /// hash-mismatch and fail verification rather than be silently
    /// reinterpreted. Set to [`SCHEMA_VERSION`] at publish.
    pub schema_version: u32,
    /// Human-readable topic name. Validated by [`validate_topic_name`] at
    /// publish; the topic_id derives from `(author_puddle_pubkey, topic_name)`
    /// so a v1 founder-only message implicitly anchors itself to a topic
    /// keyed by its own author pubkey.
    pub topic_name: String,
    /// Founder-monotonic sequence number. Inside the signed canonical bytes
    /// so a reordering attack cannot mutate it without invalidating the
    /// signature.
    pub seq: u64,
    /// The message body. UTF-8 text; operators encode binary payloads (JSON,
    /// base64, etc.) at the application layer. Size-capped at publish: the
    /// operator's `swarm.poolq.max_msg_bytes` tunes inside `[0,
    /// HARD_MAX_MSG_BYTES]`; bigger payloads belong in a curator artifact
    /// that the message references by id.
    pub body: String,
    /// Unix milliseconds when the message was issued. Inside the signed
    /// bytes; consumers compare against the operator's retention window for
    /// the stale-on-ingest refusal that makes age-based pruning flap-stable.
    pub posted_at: i64,
    /// Hex-encoded raw Ed25519 puddle pubkey of the signer. In v1 founder-
    /// only, this IS the topic founder; in v2 with delegation, it's the
    /// current signing identity and the delegation chain bridges it back to
    /// the genesis founder.
    pub author_puddle_pubkey: String,
}

/// A signed poolq message: body + Ed25519 signature over the canonical bytes.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Message {
    pub body: MessageBody,
    /// Hex-encoded raw 64-byte Ed25519 signature over [`canonical_bytes`].
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

fn put_body(out: &mut Vec<u8>, body: &str) -> Result<()> {
    let b = body.as_bytes();
    // The runtime cap is HARD_MAX_MSG_BYTES (64 KiB), well under u32::MAX —
    // u32 here is defense in depth so a single rogue caller crafting a 4 GiB
    // body doesn't silently truncate via a u16 width.
    if b.len() > u32::MAX as usize {
        bail!("message body too long for canonical encoding ({} bytes)", b.len());
    }
    out.extend_from_slice(&(b.len() as u32).to_be_bytes());
    out.extend_from_slice(b);
    Ok(())
}

/// Hand-rolled deterministic encoding for [`MessageBody`]. Layout (after the
/// [`POOLQ_MSG_DOMAIN`] prefix):
///
/// 1. `schema_version` (4 B BE)
/// 2. `author_puddle_pubkey` (32 B raw, parsed from hex)
/// 3. `topic_name` (u16 BE length + UTF-8 bytes)
/// 4. `seq` (8 B BE u64)
/// 5. `posted_at` (8 B BE i64)
/// 6. `body` (u32 BE length + UTF-8 bytes)
///
/// `seq` is **inside** the signed bytes so a reordering attempt cannot
/// mutate it without invalidating the signature.
pub fn canonical_bytes(body: &MessageBody) -> Result<Vec<u8>> {
    let author = parse_hex32(&body.author_puddle_pubkey, "author_puddle_pubkey")?;
    let topic = body.topic_name.as_bytes();
    let payload = body.body.as_bytes();
    let mut out = Vec::with_capacity(
        POOLQ_MSG_DOMAIN.len() + 4 + 32 + 2 + topic.len() + 8 + 8 + 4 + payload.len(),
    );
    out.extend_from_slice(POOLQ_MSG_DOMAIN);
    out.extend_from_slice(&body.schema_version.to_be_bytes());
    out.extend_from_slice(&author);
    put_str(&mut out, &body.topic_name, "topic_name")?;
    out.extend_from_slice(&body.seq.to_be_bytes());
    out.extend_from_slice(&body.posted_at.to_be_bytes());
    put_body(&mut out, &body.body)?;
    Ok(out)
}

impl Message {
    /// Issue + sign a message with a [`SigningKey`] held in process.
    /// `body.author_puddle_pubkey` MUST equal the signing key's public half;
    /// re-checked here so a misuse fails loudly. Self-verifies before
    /// returning so a freshly-built message that wouldn't accept can never
    /// leave this function.
    pub fn issue(signing_key: &SigningKey, body: MessageBody) -> Result<Message> {
        let expect = hex::encode(signing_key.verifying_key().to_bytes());
        if body.author_puddle_pubkey.to_ascii_lowercase() != expect {
            bail!(
                "message author_puddle_pubkey ({}) does not match the signing key's public half ({})",
                body.author_puddle_pubkey,
                expect
            );
        }
        Self::issue_with(body, |msg| Ok(signing_key.sign(msg).to_bytes()))
    }

    /// Issue + sign with a delegated signer (e.g. the puddle session helper,
    /// used by `vigo swarm poolq post` — Slice 4). `sign` receives the
    /// canonical bytes and returns the raw 64-byte signature. Self-verifies
    /// before returning.
    pub fn issue_with<F>(body: MessageBody, sign: F) -> Result<Message>
    where
        F: FnOnce(&[u8]) -> Result<[u8; SIG_LEN]>,
    {
        let msg = canonical_bytes(&body)?;
        let sig = sign(&msg)?;
        let entry = Message {
            body,
            signature: hex::encode(sig),
        };
        entry
            .verify_signature()
            .context("freshly-built message failed self-verification")?;
        Ok(entry)
    }

    /// The topic this message anchors to:
    /// `sha256(author_puddle_pubkey ‖ topic_name)`. In v1 founder-only,
    /// this equals the topic's id; in v2 with delegation a separate
    /// check would resolve through the chain.
    pub fn topic_id(&self) -> Result<[u8; ID_LEN]> {
        let author = parse_hex32(&self.body.author_puddle_pubkey, "author_puddle_pubkey")?;
        Ok(topic_id_for(&author, &self.body.topic_name))
    }

    /// Hex form of [`Self::topic_id`].
    pub fn topic_id_hex(&self) -> Result<String> {
        Ok(hex::encode(self.topic_id()?))
    }

    /// `msg_id = sha256(canonical_bytes)`. The G-Set CRDT's merge key.
    pub fn msg_id(&self) -> Result<[u8; ID_LEN]> {
        Ok(msg_id_for(&canonical_bytes(&self.body)?))
    }

    /// Hex form of [`Self::msg_id`].
    pub fn msg_id_hex(&self) -> Result<String> {
        Ok(hex::encode(self.msg_id()?))
    }

    /// Verify the Ed25519 signature against the canonical bytes under
    /// `author_puddle_pubkey`. Does NOT enforce founder-only — that's a
    /// per-topic policy the mesh applies by rejecting any message whose
    /// `topic_id` (derived from THIS message's `author_puddle_pubkey` +
    /// `topic_name`) differs from the topic's first-seen anchor.
    pub fn verify_signature(&self) -> Result<()> {
        let author_bytes = parse_hex32(&self.body.author_puddle_pubkey, "author_puddle_pubkey")?;
        let sig_bytes = parse_hex64(&self.signature, "signature")?;
        let pubkey = VerifyingKey::from_bytes(&author_bytes)
            .map_err(|e| anyhow!("malformed author_puddle_pubkey: {e}"))?;
        let canonical = canonical_bytes(&self.body)?;
        pubkey
            .verify(&canonical, &ed25519_dalek::Signature::from_bytes(&sig_bytes))
            .map_err(|e| anyhow!("signature does not verify: {e}"))?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    fn fresh_body(key: &SigningKey, topic: &str, seq: u64, body: &str) -> MessageBody {
        MessageBody {
            schema_version: SCHEMA_VERSION,
            topic_name: topic.into(),
            seq,
            body: body.into(),
            posted_at: 1_700_000_000_000,
            author_puddle_pubkey: hex::encode(key.verifying_key().to_bytes()),
        }
    }

    #[test]
    fn topic_id_is_stable() {
        let pubkey = [0xABu8; ID_LEN];
        let id1 = topic_id_for(&pubkey, "build-tasks");
        let id2 = topic_id_for(&pubkey, "build-tasks");
        assert_eq!(id1, id2);
        let different_name = topic_id_for(&pubkey, "deploy-tasks");
        assert_ne!(id1, different_name);
        let different_key = topic_id_for(&[0xCDu8; ID_LEN], "build-tasks");
        assert_ne!(id1, different_key);
    }

    #[test]
    fn validate_topic_name_rejects_bad_inputs() {
        // Good shapes.
        assert!(validate_topic_name("build-tasks").is_ok());
        assert!(validate_topic_name("a").is_ok());
        assert!(validate_topic_name("build_tasks.v2").is_ok());
        // Empty + too long.
        assert!(validate_topic_name("").is_err());
        assert!(validate_topic_name(&"a".repeat(65)).is_err());
        // Leading dot.
        assert!(validate_topic_name(".hidden").is_err());
        // 64-hex collision with topic_id form.
        assert!(validate_topic_name(&"a".repeat(64)).is_err());
        // Disallowed characters.
        assert!(validate_topic_name("build tasks").is_err());
        assert!(validate_topic_name("build/tasks").is_err());
        assert!(validate_topic_name("build@tasks").is_err());
    }

    #[test]
    fn issue_and_verify_round_trip() {
        let key = SigningKey::generate(&mut OsRng);
        let body = fresh_body(&key, "build-tasks", 1, "hello");
        let msg = Message::issue(&key, body).expect("issue");
        msg.verify_signature().expect("verify");
        // topic_id is self-anchoring.
        let expected_topic = topic_id_for(&key.verifying_key().to_bytes(), "build-tasks");
        assert_eq!(msg.topic_id().unwrap(), expected_topic);
        // msg_id is content-addressed.
        let id1 = msg.msg_id_hex().unwrap();
        let id2 = msg.msg_id_hex().unwrap();
        assert_eq!(id1, id2);
        assert_eq!(id1.len(), 64);
    }

    #[test]
    fn issue_rejects_pubkey_mismatch() {
        let key = SigningKey::generate(&mut OsRng);
        let other = SigningKey::generate(&mut OsRng);
        let mut body = fresh_body(&other, "build-tasks", 1, "hello");
        // The author_pubkey says `other`, but we sign with `key` — `issue`
        // refuses, because the resulting envelope would not verify against
        // its own author_pubkey.
        body.author_puddle_pubkey = hex::encode(other.verifying_key().to_bytes());
        assert!(Message::issue(&key, body).is_err());
    }

    #[test]
    fn tampered_body_fails_verification() {
        let key = SigningKey::generate(&mut OsRng);
        let body = fresh_body(&key, "build-tasks", 1, "hello");
        let mut msg = Message::issue(&key, body).expect("issue");
        msg.body.body = "tampered".into();
        // The signature was computed over the original bytes; mutating the
        // body invalidates verification.
        assert!(msg.verify_signature().is_err());
    }

    #[test]
    fn tampered_seq_fails_verification() {
        let key = SigningKey::generate(&mut OsRng);
        let body = fresh_body(&key, "build-tasks", 5, "hello");
        let mut msg = Message::issue(&key, body).expect("issue");
        msg.body.seq = 6;
        // `seq` is inside canonical bytes — a reordering attempt fails the
        // signature.
        assert!(msg.verify_signature().is_err());
    }

    #[test]
    fn domain_separator_is_in_canonical_bytes() {
        let key = SigningKey::generate(&mut OsRng);
        let body = fresh_body(&key, "build-tasks", 1, "hello");
        let bytes = canonical_bytes(&body).unwrap();
        assert!(bytes.starts_with(POOLQ_MSG_DOMAIN), "canonical bytes must start with POOLQ_MSG_DOMAIN");
    }

    #[test]
    fn schema_version_is_signed() {
        let key = SigningKey::generate(&mut OsRng);
        let body = fresh_body(&key, "build-tasks", 1, "hello");
        let mut msg = Message::issue(&key, body).expect("issue");
        msg.body.schema_version = SCHEMA_VERSION + 1;
        // Bumping schema_version after issue must fail verification: a v2
        // reader cannot re-interpret a v1 envelope under the new shape
        // silently.
        assert!(msg.verify_signature().is_err());
    }

    /// Cross-language interop pin. The same `MessageBody` fields are pinned
    /// to the same hex `msg_id` in `server/swarm/poolqmesh/poolqmesh_test.go`'s
    /// `TestInteropMsgIDPin`. If either implementation's canonical-bytes
    /// layout drifts (re-ordering fields, changing length-prefix widths,
    /// changing the domain string), exactly one of the two tests fails
    /// before the drift can land in a release.
    #[test]
    fn interop_pin_msg_id_matches_server_side() {
        let body = MessageBody {
            schema_version: 1,
            topic_name: "build-tasks".into(),
            seq: 42,
            body: "hello".into(),
            posted_at: 1_700_000_000_123,
            author_puddle_pubkey: hex::encode([0x42u8; 32]),
        };
        let bytes = canonical_bytes(&body).expect("canonical_bytes");
        let id = msg_id_for(&bytes);
        // Hand-computed via the layout in `canonical_bytes`'s doc comment
        // and the matching Python sha256 in the slice-3 work notes.
        assert_eq!(
            hex::encode(id),
            "5f229a41d3f68199e00c3afac92455957785affe6c5cd997ed4520626bfbf264",
            "canonical-bytes layout drifted on the Rust side"
        );
    }

    #[test]
    fn issue_with_threads_a_delegated_signer() {
        let key = SigningKey::generate(&mut OsRng);
        let body = fresh_body(&key, "build-tasks", 1, "hello");
        let msg = Message::issue_with(body, |canonical| Ok(key.sign(canonical).to_bytes()))
            .expect("issue_with");
        msg.verify_signature().expect("verify");
    }
}
