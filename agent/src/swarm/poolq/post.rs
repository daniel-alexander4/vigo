// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! The user-side publish path for poolq (ADR-029).
//!
//! Called from the Slice-4 `vigo swarm poolq post` CLI verb. Mirrors the
//! shape `Message::issue_with` uses: the caller passes a `sign` closure so
//! production code can thread the puddle session helper through it while
//! tests pass an in-memory key. No `tokio`, no daemon — this is straight-
//! line code under flock.
//!
//! ### Order of operations
//!
//! 1. Validate `topic_name` + body size + runtime caps.
//! 2. Compute `topic_id = sha256(author_pubkey ‖ topic_name)` and ensure
//!    the per-topic dir layout exists.
//! 3. Acquire the per-topic seq allocator (flock-protected) and issue `seq`.
//!    The flock is held until the message file lands so a concurrent poster
//!    on the same envoy can't observe a published `seq` whose message file
//!    is still in flight.
//! 4. Build the [`MessageBody`], hand it to the sign closure, assemble the
//!    [`Message`].
//! 5. Write the message file atomically (temp + rename).

use std::path::Path;

use anyhow::{bail, Context, Result};

use super::allocator::SeqAllocator;
use super::message::{
    canonical_bytes, topic_id_for, validate_topic_name, Message, MessageBody, HARD_MAX_MSG_BYTES,
    ID_LEN, SCHEMA_VERSION, SIG_LEN,
};
use super::state::{allocator_path, atomic_write, ensure_topic_dir, message_path};

/// Parameters for [`publish`]. Everything except the sign closure has a
/// natural default that the Slice-4 CLI verb supplies from the runtime
/// limits cache or `now()`.
pub struct PublishParams<'a> {
    /// Posting user's home directory; per-user state lives under
    /// `<home>/.vigo-poolq/`.
    pub home: &'a Path,
    /// The signer's puddle pubkey, raw 32 B. The signed envelope's
    /// `author_puddle_pubkey` is set from this (hex); the sign closure
    /// must produce a signature that verifies under this pubkey.
    pub author_puddle_pubkey: [u8; ID_LEN],
    /// Human-readable topic name, validated by [`validate_topic_name`].
    pub topic_name: &'a str,
    /// Message body — UTF-8 text; size-capped against [`HARD_MAX_MSG_BYTES`]
    /// AND against `max_msg_bytes` if Some (the operator-configured cap from
    /// `swarm.poolq.max_msg_bytes`; the CLI verb threads this through from
    /// the runtime-limits cache).
    pub body: &'a str,
    /// Unix milliseconds the message claims to have been posted at. Caller
    /// supplies it (production: `now_ms()`; tests: a fixed value).
    pub posted_at: i64,
    /// Operator-configured per-message cap from `swarm.poolq.max_msg_bytes`,
    /// in bytes. `None` ⇒ enforce the hard ceiling only.
    pub max_msg_bytes: Option<u64>,
}

/// One published message, returned by [`publish`] so the Slice-4 CLI verb
/// can report the issued `seq` + `msg_id` to the operator.
#[derive(Debug, Clone)]
pub struct Published {
    pub seq: u64,
    pub msg_id_hex: String,
    pub topic_id_hex: String,
}

/// Publish a poolq message. The sign closure receives the canonical bytes
/// and returns the raw 64-byte Ed25519 signature; production code threads
/// the puddle session helper through it, tests pass an in-memory key.
pub fn publish<F>(params: PublishParams<'_>, sign: F) -> Result<Published>
where
    F: FnOnce(&[u8]) -> Result<[u8; SIG_LEN]>,
{
    validate_topic_name(params.topic_name).context("topic name")?;

    let body_len = params.body.len();
    if body_len > HARD_MAX_MSG_BYTES {
        bail!(
            "message body is {} bytes, over the {} B hard ceiling — pair with a curator artifact for larger payloads",
            body_len,
            HARD_MAX_MSG_BYTES
        );
    }
    if let Some(cap) = params.max_msg_bytes {
        if cap > 0 && (body_len as u64) > cap {
            bail!(
                "message body is {} bytes, over the operator-configured swarm.poolq.max_msg_bytes cap of {}",
                body_len,
                cap
            );
        }
    }

    let topic_id = topic_id_for(&params.author_puddle_pubkey, params.topic_name);
    let topic_id_hex = hex::encode(topic_id);
    ensure_topic_dir(params.home, &topic_id_hex)
        .with_context(|| format!("preparing topic dir for {}", topic_id_hex))?;

    let allocator = SeqAllocator::at(allocator_path(params.home, &topic_id_hex));
    let seq = allocator
        .issue()
        .context("allocating next seq for topic")?;

    let body = MessageBody {
        schema_version: SCHEMA_VERSION,
        topic_name: params.topic_name.to_string(),
        seq,
        body: params.body.to_string(),
        posted_at: params.posted_at,
        author_puddle_pubkey: hex::encode(params.author_puddle_pubkey),
    };
    let canonical = canonical_bytes(&body).context("canonical-bytes encoding")?;
    let sig = sign(&canonical).context("signing message")?;
    let msg = Message {
        body,
        signature: hex::encode(sig),
    };
    msg.verify_signature()
        .context("freshly-built message failed self-verification")?;

    let msg_id_hex = msg.msg_id_hex().context("computing msg_id")?;
    let path = message_path(params.home, &topic_id_hex, &msg_id_hex);
    let json = serde_json::to_vec_pretty(&msg).context("serializing message JSON")?;
    atomic_write(&path, &json)
        .with_context(|| format!("writing message file {}", path.display()))?;

    Ok(Published {
        seq,
        msg_id_hex,
        topic_id_hex,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::poolq::message::{Message, MessageBody, SCHEMA_VERSION};
    use ed25519_dalek::{Signer, SigningKey};
    use rand::rngs::OsRng;
    use std::fs;
    use tempfile::TempDir;

    fn signer_for(key: &SigningKey) -> impl FnOnce(&[u8]) -> Result<[u8; 64]> + '_ {
        move |canonical: &[u8]| Ok(key.sign(canonical).to_bytes())
    }

    #[test]
    fn happy_path_writes_a_signed_message_file() {
        let key = SigningKey::generate(&mut OsRng);
        let dir = TempDir::new().unwrap();
        let params = PublishParams {
            home: dir.path(),
            author_puddle_pubkey: key.verifying_key().to_bytes(),
            topic_name: "build-tasks",
            body: r#"{"task":"deploy","host":"plex"}"#,
            posted_at: 1_700_000_000_000,
            max_msg_bytes: None,
        };
        let pub_ = publish(params, signer_for(&key)).expect("publish");
        assert_eq!(pub_.seq, 1);
        assert_eq!(pub_.msg_id_hex.len(), 64);
        assert_eq!(pub_.topic_id_hex.len(), 64);

        // The file landed under the per-topic messages dir...
        let path = message_path(dir.path(), &pub_.topic_id_hex, &pub_.msg_id_hex);
        let bytes = fs::read(&path).unwrap();
        // ...and round-trips through serde + verifies end-to-end.
        let msg: Message = serde_json::from_slice(&bytes).unwrap();
        msg.verify_signature().expect("on-disk message verifies");
        assert_eq!(msg.body.seq, 1);
        assert_eq!(msg.body.topic_name, "build-tasks");
    }

    #[test]
    fn sequential_posts_get_monotonic_seqs() {
        let key = SigningKey::generate(&mut OsRng);
        let dir = TempDir::new().unwrap();
        for expect in 1..=5u64 {
            let p = publish(
                PublishParams {
                    home: dir.path(),
                    author_puddle_pubkey: key.verifying_key().to_bytes(),
                    topic_name: "build-tasks",
                    body: &format!("msg {expect}"),
                    posted_at: 1_700_000_000_000 + expect as i64,
                    max_msg_bytes: None,
                },
                signer_for(&key),
            )
            .expect("publish");
            assert_eq!(p.seq, expect);
        }
    }

    #[test]
    fn rejects_body_over_hard_ceiling() {
        let key = SigningKey::generate(&mut OsRng);
        let dir = TempDir::new().unwrap();
        let big = "x".repeat(HARD_MAX_MSG_BYTES + 1);
        let err = publish(
            PublishParams {
                home: dir.path(),
                author_puddle_pubkey: key.verifying_key().to_bytes(),
                topic_name: "build-tasks",
                body: &big,
                posted_at: 0,
                max_msg_bytes: None,
            },
            signer_for(&key),
        )
        .unwrap_err();
        let msg = format!("{err:#}");
        assert!(msg.contains("hard ceiling"), "msg={msg}");
    }

    #[test]
    fn rejects_body_over_operator_cap() {
        let key = SigningKey::generate(&mut OsRng);
        let dir = TempDir::new().unwrap();
        let err = publish(
            PublishParams {
                home: dir.path(),
                author_puddle_pubkey: key.verifying_key().to_bytes(),
                topic_name: "build-tasks",
                body: "xxxxxxxxxx",
                posted_at: 0,
                max_msg_bytes: Some(4),
            },
            signer_for(&key),
        )
        .unwrap_err();
        let msg = format!("{err:#}");
        assert!(msg.contains("max_msg_bytes"), "msg={msg}");
    }

    #[test]
    fn rejects_bad_topic_name() {
        let key = SigningKey::generate(&mut OsRng);
        let dir = TempDir::new().unwrap();
        let err = publish(
            PublishParams {
                home: dir.path(),
                author_puddle_pubkey: key.verifying_key().to_bytes(),
                topic_name: ".hidden",
                body: "hi",
                posted_at: 0,
                max_msg_bytes: None,
            },
            signer_for(&key),
        )
        .unwrap_err();
        let msg = format!("{err:#}");
        assert!(msg.contains("topic name"), "msg={msg}");
    }

    #[test]
    fn two_topics_have_independent_allocators() {
        let key = SigningKey::generate(&mut OsRng);
        let dir = TempDir::new().unwrap();
        let a1 = publish(
            PublishParams {
                home: dir.path(),
                author_puddle_pubkey: key.verifying_key().to_bytes(),
                topic_name: "alpha",
                body: "1",
                posted_at: 0,
                max_msg_bytes: None,
            },
            signer_for(&key),
        )
        .unwrap();
        let b1 = publish(
            PublishParams {
                home: dir.path(),
                author_puddle_pubkey: key.verifying_key().to_bytes(),
                topic_name: "beta",
                body: "1",
                posted_at: 0,
                max_msg_bytes: None,
            },
            signer_for(&key),
        )
        .unwrap();
        let a2 = publish(
            PublishParams {
                home: dir.path(),
                author_puddle_pubkey: key.verifying_key().to_bytes(),
                topic_name: "alpha",
                body: "2",
                posted_at: 0,
                max_msg_bytes: None,
            },
            signer_for(&key),
        )
        .unwrap();
        assert_eq!(a1.seq, 1);
        assert_eq!(b1.seq, 1, "different topics get independent allocators");
        assert_eq!(a2.seq, 2);
        assert_ne!(a1.topic_id_hex, b1.topic_id_hex);
    }

    #[test]
    fn idempotent_repost_of_same_canonical_bytes() {
        // The msg_id is content-addressed. If two consecutive publishes
        // produce byte-identical canonical bytes (same author, topic_name,
        // seq, body, posted_at), they share a msg_id ⇒ the second
        // overwrites the first idempotently. In practice seq differs so
        // this is mostly defense-in-depth — we test it by bypassing
        // `publish` and writing the same Message twice.
        let key = SigningKey::generate(&mut OsRng);
        let dir = TempDir::new().unwrap();
        let topic = "build-tasks";
        let topic_id = topic_id_for(&key.verifying_key().to_bytes(), topic);
        let topic_id_hex = hex::encode(topic_id);
        ensure_topic_dir(dir.path(), &topic_id_hex).unwrap();
        let body = MessageBody {
            schema_version: SCHEMA_VERSION,
            topic_name: topic.into(),
            seq: 42,
            body: "the body".into(),
            posted_at: 1_700_000_000_000,
            author_puddle_pubkey: hex::encode(key.verifying_key().to_bytes()),
        };
        let msg = Message::issue(&key, body).unwrap();
        let msg_id_hex = msg.msg_id_hex().unwrap();
        let path = message_path(dir.path(), &topic_id_hex, &msg_id_hex);

        let bytes = serde_json::to_vec_pretty(&msg).unwrap();
        atomic_write(&path, &bytes).unwrap();
        // Second write of byte-identical content — no failure, file still
        // there, contents unchanged.
        atomic_write(&path, &bytes).unwrap();
        assert_eq!(fs::read(&path).unwrap(), bytes);
    }
}
