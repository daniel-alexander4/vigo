// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-user / per-topic on-disk paths for poolq, and the atomic write helpers
//! the publish path uses. The Slice-3 trait collector walks this layout
//! across users and emits each message file verbatim into the `swarm_poolq`
//! trait namespace.
//!
//! ### Layout
//!
//! Under each posting user's home (e.g. `/home/dan/.vigo-poolq/`):
//!
//! ```text
//! .vigo-poolq/
//!   <topic_id_hex>/
//!     allocator.json                    # SeqAllocator state, flock-protected
//!     messages/
//!       <msg_id_hex>.json               # one Message JSON per posted message
//! ```
//!
//! ### Why per-user
//!
//! poolq inherits the per-user puddle identity (ADR-014, ADR-029): a user's
//! puddle key is what signs messages, so the per-user state directory is the
//! natural home. Consumer subscription state (offsets + filters) lives at
//! `~/.vigo-poolq/subscriptions.json` — also per-user, never replicated
//! (consumer offsets are local-only by design; that's what makes the
//! Kafka-style model fit a CRDT substrate with no consensus). Slice 4 adds
//! the subscription file shape.
//!
//! ### Permissions
//!
//! Every file is created mode 0644, every dir mode 0755 (user-owned). The
//! trait collector runs as root and can read everything; non-owner users on
//! the same envoy can read message files but not modify them — fleet-
//! readable at rest is the design (ADR-029: "confidentiality is not a goal,
//! pair with lockbox for confidential payloads").

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result};

/// Per-user poolq state root, conventionally `~/.vigo-poolq/`. Tests use the
/// `_at` variants below to pass a tempdir.
pub fn user_state_dir(home: &Path) -> PathBuf {
    home.join(".vigo-poolq")
}

/// `~/.vigo-poolq/<topic_id_hex>/` — the per-topic dir for a single posting
/// user. Created by [`ensure_topic_dir`] at first post.
pub fn topic_dir(home: &Path, topic_id_hex: &str) -> PathBuf {
    user_state_dir(home).join(topic_id_hex)
}

/// `~/.vigo-poolq/<topic_id_hex>/messages/` — the per-topic messages dir.
pub fn messages_dir(home: &Path, topic_id_hex: &str) -> PathBuf {
    topic_dir(home, topic_id_hex).join("messages")
}

/// `~/.vigo-poolq/<topic_id_hex>/allocator.json` — the founder-side seq
/// allocator file. Flock-protected by [`crate::swarm::poolq::SeqAllocator`].
pub fn allocator_path(home: &Path, topic_id_hex: &str) -> PathBuf {
    topic_dir(home, topic_id_hex).join("allocator.json")
}

/// `~/.vigo-poolq/<topic_id_hex>/messages/<msg_id_hex>.json` — one Message
/// JSON per posted message. Content-addressed: a message's bytes determine
/// its filename, so a re-post of an identical body is a no-op overwrite
/// (the file already exists, the rename below is idempotent).
pub fn message_path(home: &Path, topic_id_hex: &str, msg_id_hex: &str) -> PathBuf {
    messages_dir(home, topic_id_hex).join(format!("{msg_id_hex}.json"))
}

/// Create the per-topic dir layout (`<home>/.vigo-poolq/<topic_id>/messages/`)
/// idempotently. Each directory is 0755; the parent home is left as-is
/// (assumed to exist with whatever mode the OS gave it).
pub fn ensure_topic_dir(home: &Path, topic_id_hex: &str) -> Result<()> {
    let messages = messages_dir(home, topic_id_hex);
    fs::create_dir_all(&messages)
        .with_context(|| format!("creating {}", messages.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let perms = fs::Permissions::from_mode(0o755);
        let _ = fs::set_permissions(user_state_dir(home), perms.clone());
        let _ = fs::set_permissions(topic_dir(home, topic_id_hex), perms.clone());
        let _ = fs::set_permissions(&messages, perms);
    }
    Ok(())
}

/// Atomic write to `path`: temp + rename so a concurrent reader (the trait
/// collector) never sees a half-written file. Mode 0644 on the final file.
///
/// Idempotent: if `path` already exists with identical bytes, the rename
/// still lands and the final file is byte-identical (content-addressed
/// message files: same msg_id ⇒ same bytes ⇒ no observable change).
pub fn atomic_write(path: &Path, bytes: &[u8]) -> Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .with_context(|| format!("creating {}", parent.display()))?;
    }
    let tmp = path.with_extension("json.tmp");
    {
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .open(&tmp)
            .with_context(|| format!("opening {} for write", tmp.display()))?;
        f.write_all(bytes)
            .with_context(|| format!("writing {}", tmp.display()))?;
        f.sync_data().ok();
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(&tmp, fs::Permissions::from_mode(0o644));
    }
    fs::rename(&tmp, path)
        .with_context(|| format!("renaming {} -> {}", tmp.display(), path.display()))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn paths_compose_predictably() {
        let home = Path::new("/home/dan");
        assert_eq!(user_state_dir(home), PathBuf::from("/home/dan/.vigo-poolq"));
        assert_eq!(
            topic_dir(home, "ab12"),
            PathBuf::from("/home/dan/.vigo-poolq/ab12")
        );
        assert_eq!(
            messages_dir(home, "ab12"),
            PathBuf::from("/home/dan/.vigo-poolq/ab12/messages")
        );
        assert_eq!(
            allocator_path(home, "ab12"),
            PathBuf::from("/home/dan/.vigo-poolq/ab12/allocator.json")
        );
        assert_eq!(
            message_path(home, "ab12", "cd34"),
            PathBuf::from("/home/dan/.vigo-poolq/ab12/messages/cd34.json")
        );
    }

    #[test]
    fn ensure_topic_dir_is_idempotent() {
        let dir = TempDir::new().unwrap();
        ensure_topic_dir(dir.path(), "topic1").unwrap();
        // Second call must not fail.
        ensure_topic_dir(dir.path(), "topic1").unwrap();
        assert!(messages_dir(dir.path(), "topic1").is_dir());
    }

    #[test]
    fn atomic_write_round_trips() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("sub/file.json");
        atomic_write(&path, b"hello").unwrap();
        assert_eq!(fs::read(&path).unwrap(), b"hello");
        // Overwrite is fine.
        atomic_write(&path, b"world").unwrap();
        assert_eq!(fs::read(&path).unwrap(), b"world");
        // No .tmp left behind.
        assert!(!path.with_extension("json.tmp").exists());
    }
}
