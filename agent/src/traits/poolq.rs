// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports poolq-posted messages for the `poolq` trait namespace (ADR-029).
//!
//! Walks every Unix user's `~/.vigo-poolq/<topic_id>/messages/*.json` and
//! emits each signed [`crate::swarm::poolq::Message`] verbatim. The server's
//! `poolqmesh` aggregator buckets them by computed `topic_id`, dedups on
//! `msg_id` (G-Set merge), and applies age-prune + stale-on-ingest refusal;
//! the agent and `vigocli swarm poolq read` verify the signature
//! end-to-end without trusting the server — same "server publishes, never
//! arbitrates" stance gitback's name claims and curator's catalog take. A
//! locally-corrupt, misfiled (dir name ≠ `topic_id`), or non-self-verifying
//! message is dropped here rather than published.
//!
//! Top-level shape (`Value::Null` ⇒ no poolq state on this envoy, so no
//! trait):
//!
//! ```jsonc
//! { "updated_at": <unix-seconds>,
//!   "message_count": 3,
//!   "messages": [ { ...full Message... }, ... ] }
//! ```

use std::collections::BTreeMap;
use std::path::Path;

use anyhow::Result;
use serde_json::{json, Value};

use super::gitback::passwd_path;
use crate::swarm::poolq::message::Message;
use crate::swarm::poolq::state;

pub fn collect() -> Result<Value> {
    let passwd_body = match std::fs::read_to_string(passwd_path()) {
        Ok(s) => s,
        Err(_) => return Ok(Value::Null),
    };
    let messages = collect_messages(&passwd_body);
    if messages.is_empty() {
        return Ok(Value::Null);
    }
    Ok(json!({
        "updated_at": unix_seconds_now(),
        "message_count": messages.len(),
        "messages": messages,
    }))
}

/// Walk passwd, read each user's `~/.vigo-poolq/<topic_id>/messages/*.json`,
/// keep the self-verifying ones whose directory name matches their
/// `topic_id`, and collapse duplicates (the same `msg_id` held by two users
/// on this envoy → keep either; bytes are identical). Sorted by `topic_id`
/// then `seq` then `msg_id` for deterministic trait output. Pulled out for
/// testability.
pub(crate) fn collect_messages(passwd_body: &str) -> Vec<Message> {
    // Key is "<topic_id_hex>/<msg_id_hex>" so duplicates collapse and the
    // BTreeMap iteration yields a stable, sorted output.
    let mut by_id: BTreeMap<String, Message> = BTreeMap::new();
    for line in passwd_body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let home = Path::new(fields[5]);
        let dot = state::user_state_dir(home);
        let topic_dirs = match std::fs::read_dir(&dot) {
            Ok(it) => it,
            Err(_) => continue,
        };
        for topic_ent in topic_dirs.flatten() {
            let topic_path = topic_ent.path();
            if !topic_path.is_dir() {
                continue;
            }
            let topic_id_dir = match topic_path.file_name().and_then(|n| n.to_str()) {
                Some(n) => n.to_string(),
                None => continue,
            };
            let msgs_dir = topic_path.join("messages");
            let msg_files = match std::fs::read_dir(&msgs_dir) {
                Ok(it) => it,
                Err(_) => continue,
            };
            for msg_ent in msg_files.flatten() {
                let p = msg_ent.path();
                if !p.is_file() {
                    continue;
                }
                let raw = match std::fs::read_to_string(&p) {
                    Ok(s) => s,
                    Err(_) => continue,
                };
                let msg: Message = match serde_json::from_str(&raw) {
                    Ok(m) => m,
                    Err(_) => continue,
                };
                // Directory name must equal the message's own topic_id; the
                // message must self-verify; the filename's stem must equal
                // its own msg_id. All three checks together mean a tampered
                // file can never enter the fleet via this collector.
                let topic_id_hex = match msg.topic_id_hex() {
                    Ok(t) => t,
                    Err(_) => continue,
                };
                if topic_id_hex != topic_id_dir {
                    continue;
                }
                let msg_id_hex = match msg.msg_id_hex() {
                    Ok(m) => m,
                    Err(_) => continue,
                };
                let want_stem = format!("{msg_id_hex}.json");
                if p.file_name().and_then(|n| n.to_str()) != Some(want_stem.as_str()) {
                    continue;
                }
                if msg.verify_signature().is_err() {
                    continue;
                }
                let key = format!("{topic_id_hex}/{msg_id_hex}");
                by_id.entry(key).or_insert(msg);
            }
        }
    }
    by_id.into_values().collect()
}

fn unix_seconds_now() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::poolq::message::{Message, MessageBody, SCHEMA_VERSION};
    use crate::swarm::poolq::state::{ensure_topic_dir, message_path};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use tempfile::tempdir;

    fn pub_hex(k: &SigningKey) -> String {
        hex::encode(k.verifying_key().to_bytes())
    }

    fn signed_message(k: &SigningKey, topic: &str, seq: u64, body: &str, posted_at: i64) -> Message {
        let body = MessageBody {
            schema_version: SCHEMA_VERSION,
            topic_name: topic.into(),
            seq,
            body: body.into(),
            posted_at,
            author_puddle_pubkey: pub_hex(k),
        };
        Message::issue(k, body).unwrap()
    }

    fn write_message_under(home: &Path, msg: &Message) {
        let topic_id_hex = msg.topic_id_hex().unwrap();
        let msg_id_hex = msg.msg_id_hex().unwrap();
        ensure_topic_dir(home, &topic_id_hex).unwrap();
        let path = message_path(home, &topic_id_hex, &msg_id_hex);
        std::fs::write(&path, serde_json::to_string(msg).unwrap()).unwrap();
    }

    fn passwd_line(user: &str, uid: u32, home: &Path) -> String {
        format!("{user}:x:{uid}:{uid}::{}:/bin/sh\n", home.display())
    }

    #[test]
    fn empty_when_no_state() {
        let tmp = tempdir().unwrap();
        assert!(collect_messages(&passwd_line("dan", 1000, tmp.path())).is_empty());
        assert!(collect_messages("garbage\n#comment\n").is_empty());
    }

    #[test]
    fn picks_up_a_valid_message() {
        let tmp = tempdir().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let m = signed_message(&k, "build-tasks", 1, "hi", 1_700_000_000_000);
        write_message_under(tmp.path(), &m);
        let got = collect_messages(&passwd_line("dan", 1000, tmp.path()));
        assert_eq!(got.len(), 1);
        got[0].verify_signature().unwrap();
        assert_eq!(got[0].body.topic_name, "build-tasks");
        assert_eq!(got[0].body.seq, 1);
    }

    #[test]
    fn drops_corrupt_misfiled_and_tampered_messages() {
        let tmp = tempdir().unwrap();
        let k = SigningKey::generate(&mut OsRng);

        // 1. Not JSON.
        let bogus_topic_dir = tmp.path().join(".vigo-poolq").join("a".repeat(64));
        std::fs::create_dir_all(bogus_topic_dir.join("messages")).unwrap();
        std::fs::write(
            bogus_topic_dir.join("messages").join("badname.json"),
            b"not json",
        )
        .unwrap();

        // 2. Valid message filed under the wrong topic_id directory.
        let valid = signed_message(&k, "x", 1, "ok", 1_700_000_000_000);
        let wrong_topic = "b".repeat(64);
        ensure_topic_dir(tmp.path(), &wrong_topic).unwrap();
        let valid_msg_id = valid.msg_id_hex().unwrap();
        let wrong_path = message_path(tmp.path(), &wrong_topic, &valid_msg_id);
        std::fs::write(&wrong_path, serde_json::to_string(&valid).unwrap()).unwrap();

        // 3. Tampered body: msg_id recomputes to match dir/file naming, so
        //    only the signature check catches it. We write a message whose
        //    filename matches its post-tamper msg_id but whose signature was
        //    over the pre-tamper bytes.
        let m = signed_message(&k, "y", 1, "original", 1_700_000_000_000);
        let mut tampered = m.clone();
        tampered.body.body = "tampered".into();
        let tampered_topic = tampered.topic_id_hex().unwrap();
        let tampered_msg = tampered.msg_id_hex().unwrap();
        ensure_topic_dir(tmp.path(), &tampered_topic).unwrap();
        let tampered_path = message_path(tmp.path(), &tampered_topic, &tampered_msg);
        std::fs::write(&tampered_path, serde_json::to_string(&tampered).unwrap()).unwrap();

        // 4. Mismatched filename: valid message but filed under wrong msg_id name.
        let weird_name = signed_message(&k, "z", 1, "weird", 1_700_000_000_000);
        let weird_topic = weird_name.topic_id_hex().unwrap();
        ensure_topic_dir(tmp.path(), &weird_topic).unwrap();
        let mistyped = message_path(tmp.path(), &weird_topic, &"d".repeat(64));
        std::fs::write(&mistyped, serde_json::to_string(&weird_name).unwrap()).unwrap();

        assert!(collect_messages(&passwd_line("dan", 1000, tmp.path())).is_empty());
    }

    #[test]
    fn dedups_identical_messages_across_users() {
        // Two users sharing one puddle key → same message bytes for the
        // same (seq, body, posted_at) → identical msg_id → one row out.
        let tmp1 = tempdir().unwrap();
        let tmp2 = tempdir().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let m = signed_message(&k, "shared", 1, "x", 1_700_000_000_000);
        write_message_under(tmp1.path(), &m);
        write_message_under(tmp2.path(), &m);
        let passwd = format!(
            "{}{}",
            passwd_line("dan", 1000, tmp1.path()),
            passwd_line("ann", 1001, tmp2.path()),
        );
        let got = collect_messages(&passwd);
        assert_eq!(got.len(), 1, "G-Set merge by msg_id collapses duplicates");
    }

    #[test]
    fn returns_messages_in_deterministic_order() {
        let tmp = tempdir().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let m1 = signed_message(&k, "alpha", 1, "a1", 1_700_000_000_001);
        let m2 = signed_message(&k, "alpha", 2, "a2", 1_700_000_000_002);
        let m3 = signed_message(&k, "beta", 1, "b1", 1_700_000_000_003);
        write_message_under(tmp.path(), &m1);
        write_message_under(tmp.path(), &m2);
        write_message_under(tmp.path(), &m3);
        let got = collect_messages(&passwd_line("dan", 1000, tmp.path()));
        assert_eq!(got.len(), 3);
        // Sorted by (topic_id_hex, msg_id_hex) via the BTreeMap key. Order
        // between runs is stable for a given set of inputs.
        let id1 = got[0].topic_id_hex().unwrap();
        let id2 = got[1].topic_id_hex().unwrap();
        let id3 = got[2].topic_id_hex().unwrap();
        let mut sorted = vec![id1.clone(), id2.clone(), id3.clone()];
        sorted.sort();
        assert_eq!(vec![id1, id2, id3], sorted);
    }
}
