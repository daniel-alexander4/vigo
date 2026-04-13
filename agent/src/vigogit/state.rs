// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigogit on-disk state.
//!
//! The source side persists `last_seeded_sha` per label so the agent
//! does not re-publish an identical bundle after a restart. State
//! lives at `<base>/state.json` (where `base` is
//! [`crate::platform::paths::default_vigogit_dir`]).
//!
//! Bundle scratch files live under `<base>/tmp/`. Orphan cleanup on
//! agent startup deletes any `.bundle` files older than
//! [`STALE_BUNDLE_AGE`].
//!
//! This module provides the scratch-dir helpers. The full
//! `state.json` reader/writer is added alongside `source.rs` in a
//! later commit — for scaffolding, only the tmp-dir helpers are
//! needed.

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};
use tracing::{debug, warn};

/// Age at which an orphan bundle file under `tmp/` is considered
/// stale and eligible for removal on agent startup.
pub const STALE_BUNDLE_AGE: Duration = Duration::from_secs(60 * 60);

/// Resolve the bundle scratch directory relative to the vigogit
/// state base directory.
pub fn tmp_dir(base: &Path) -> PathBuf {
    base.join("tmp")
}

/// Delete any `*.bundle` file under `<base>/tmp/` older than
/// [`STALE_BUNDLE_AGE`]. Called on agent startup to clean up after
/// interrupted bundle-create operations.
pub fn cleanup_stale_bundles(base: &Path) {
    let cutoff = match SystemTime::now().checked_sub(STALE_BUNDLE_AGE) {
        Some(t) => t,
        None => return,
    };
    cleanup_stale_bundles_with_cutoff(base, cutoff);
}

/// Testable variant: removes `*.bundle` files whose mtime is older
/// than `cutoff`.
fn cleanup_stale_bundles_with_cutoff(base: &Path, cutoff: SystemTime) {
    let dir = tmp_dir(base);
    if !dir.exists() {
        return;
    }
    let entries = match fs::read_dir(&dir) {
        Ok(e) => e,
        Err(e) => {
            warn!(dir = %dir.display(), error = %e, "vigogit: cannot read tmp dir");
            return;
        }
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.extension().and_then(|s| s.to_str()) != Some("bundle") {
            continue;
        }
        let modified = match entry.metadata().and_then(|m| m.modified()) {
            Ok(m) => m,
            Err(_) => continue,
        };
        if modified < cutoff {
            match fs::remove_file(&path) {
                Ok(_) => {
                    debug!(path = %path.display(), "vigogit: removed stale bundle")
                }
                Err(e) => {
                    warn!(path = %path.display(), error = %e, "vigogit: failed to remove stale bundle")
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    #[test]
    fn tmp_dir_joins_base() {
        let base = Path::new("/var/lib/vigo/vigogit");
        assert_eq!(
            tmp_dir(base),
            PathBuf::from("/var/lib/vigo/vigogit/tmp")
        );
    }

    #[test]
    fn cleanup_handles_missing_dir() {
        let base = tempdir().unwrap();
        // Don't create tmp dir.
        cleanup_stale_bundles(base.path()); // must not panic
    }

    #[test]
    fn cleanup_keeps_fresh_bundles() {
        let base = tempdir().unwrap();
        let tmp = tmp_dir(base.path());
        fs::create_dir_all(&tmp).unwrap();
        let fresh = tmp.join("fresh.bundle");
        fs::write(&fresh, b"new").unwrap();

        cleanup_stale_bundles(base.path());

        assert!(fresh.exists(), "freshly written bundles should survive");
    }

    #[test]
    fn cleanup_removes_stale_bundles_via_future_cutoff() {
        // Use a cutoff in the future so every file looks "older than"
        // the cutoff — this exercises the removal path without having
        // to manipulate mtimes.
        let base = tempdir().unwrap();
        let tmp = tmp_dir(base.path());
        fs::create_dir_all(&tmp).unwrap();

        let stale = tmp.join("stale.bundle");
        let notes = tmp.join("notes.txt");
        fs::write(&stale, b"data").unwrap();
        fs::write(&notes, b"keep").unwrap();

        let future = SystemTime::now() + Duration::from_secs(60);
        cleanup_stale_bundles_with_cutoff(base.path(), future);

        assert!(!stale.exists(), ".bundle files should be removed when cutoff is future");
        assert!(notes.exists(), "non-.bundle files should be preserved regardless");
    }

    #[test]
    fn cleanup_ignores_subdirs() {
        let base = tempdir().unwrap();
        let tmp = tmp_dir(base.path());
        let subdir = tmp.join("subdir.bundle"); // dir, not file
        fs::create_dir_all(&subdir).unwrap();

        let future = SystemTime::now() + Duration::from_secs(60);
        cleanup_stale_bundles_with_cutoff(base.path(), future);

        // Subdirectory should not be removed (remove_file would fail).
        assert!(subdir.exists());
    }
}
