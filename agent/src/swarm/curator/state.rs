// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Per-user on-disk layout for curator publish state (ADR-024).
//!
//! Every Unix user that has published an artifact from this envoy holds one
//! directory per artifact under their home:
//!
//! ```text
//! ~<user>/.vigo-curator/<artifact_id_hex>/
//!     entry.json          ← the canonical signed CatalogEntry (written ONLY by
//!                            the root sentinel reactor, after re-verifying it)
//!     entry.json.pending  ← a freshly-published / re-signed entry the helper
//!                            daemon dropped for the reactor to validate + promote
//! ```
//!
//! The reactor is the single writer of `entry.json`, so the `swarm_curator`
//! trait collector ([`crate::traits::curator`]) — which walks these dirs and
//! reads `entry.json` — never races a half-written file. The `.pending` →
//! `entry.json` promotion is last-write-wins on `issued_at` (matching the
//! aggregator and the gossip merge).
//!
//! Path helpers live here so the daemon, the reactor, and the trait collector
//! all agree on the layout. Same root-managed-cache-in-the-user's-home shape
//! gitback's `~/.vigo-gitback/` uses; when the shared puddle-rooted layer is
//! extracted (`anchor`, see `pending.md`) these move there alongside gitback's.

use std::path::{Path, PathBuf};

use anyhow::{anyhow, Context, Result};

/// Directory name, under each user's home, holding their curator publish state.
pub const CURATOR_DIR_NAME: &str = ".vigo-curator";

/// Filename of the canonical signed entry inside an artifact's dir.
pub const ENTRY_FILE: &str = "entry.json";

/// Filename of the pending (not-yet-promoted) entry the helper daemon writes
/// and the sentinel reactor consumes.
pub const ENTRY_PENDING_FILE: &str = "entry.json.pending";

/// `~<home>/.vigo-curator/` — the user's curator state root.
pub fn user_curator_dir(home: &Path) -> PathBuf {
    home.join(CURATOR_DIR_NAME)
}

/// `~<home>/.vigo-curator/<artifact_id_hex>/` — one artifact's dir.
pub fn artifact_dir(home: &Path, artifact_id_hex: &str) -> PathBuf {
    user_curator_dir(home).join(artifact_id_hex)
}

/// Path to the canonical `entry.json` for `artifact_id_hex` under `home`.
pub fn entry_path(home: &Path, artifact_id_hex: &str) -> PathBuf {
    artifact_dir(home, artifact_id_hex).join(ENTRY_FILE)
}

/// Path to the pending sentinel for `artifact_id_hex` under `home`.
pub fn entry_pending_path(home: &Path, artifact_id_hex: &str) -> PathBuf {
    artifact_dir(home, artifact_id_hex).join(ENTRY_PENDING_FILE)
}

/// Resolve a UID to its `(username, home)` by reading the passwd file
/// (`VIGO_GITBACK_PASSWD_FILE` override, else `/etc/passwd` — the same path
/// the gitback helper and the trait collectors use). Like the rest of the
/// agent's helper-daemon code we read the file directly rather than calling
/// `getpwuid_r(3)`, keeping the daemon's external surface tiny and off any
/// slow NSS plugin.
pub fn passwd_entry_for_uid(uid: u32) -> Result<(String, PathBuf)> {
    let path = crate::traits::gitback::passwd_path();
    let body = std::fs::read_to_string(&path)
        .with_context(|| format!("read {}", path.display()))?;
    passwd_entry_for_uid_in(&body, uid)
        .ok_or_else(|| anyhow!("no passwd entry for uid {uid}"))
}

fn passwd_entry_for_uid_in(passwd_body: &str, uid: u32) -> Option<(String, PathBuf)> {
    for line in passwd_body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        // name:passwd:uid:gid:gecos:home:shell
        let mut parts = line.splitn(7, ':');
        let name = parts.next()?;
        let _pw = parts.next()?;
        let line_uid: u32 = parts.next()?.parse().ok()?;
        let _gid = parts.next()?;
        let _gecos = parts.next()?;
        let home = parts.next()?;
        if line_uid == uid {
            return Some((name.to_string(), PathBuf::from(home)));
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn path_helpers_compose() {
        let home = Path::new("/home/dan");
        assert_eq!(user_curator_dir(home), Path::new("/home/dan/.vigo-curator"));
        assert_eq!(
            artifact_dir(home, "abcd"),
            Path::new("/home/dan/.vigo-curator/abcd")
        );
        assert_eq!(
            entry_path(home, "abcd"),
            Path::new("/home/dan/.vigo-curator/abcd/entry.json")
        );
        assert_eq!(
            entry_pending_path(home, "abcd"),
            Path::new("/home/dan/.vigo-curator/abcd/entry.json.pending")
        );
    }

    #[test]
    fn passwd_lookup_by_uid() {
        let body = "root:x:0:0::/root:/bin/bash\n\
                    # a comment\n\
                    dan:x:1000:1000:Dan:/home/dan:/bin/zsh\n\
                    svc:x:1001:1001::/var/lib/svc:/usr/sbin/nologin\n";
        assert_eq!(
            passwd_entry_for_uid_in(body, 1000),
            Some(("dan".to_string(), PathBuf::from("/home/dan")))
        );
        assert_eq!(
            passwd_entry_for_uid_in(body, 0),
            Some(("root".to_string(), PathBuf::from("/root")))
        );
        assert_eq!(passwd_entry_for_uid_in(body, 4242), None);
        assert_eq!(passwd_entry_for_uid_in("garbage\nno colons here\n", 0), None);
    }
}
