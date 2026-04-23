// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Source-side discovery for cairn repos.
//!
//! This module walks every local user's `~/cairn/*` directory and
//! classifies each subdirectory as a valid source repo, a non-owner
//! repo (operator has their own clone, we leave it alone), or an
//! error case. The result is the input to the publisher loop that
//! will live in a later commit — source.rs currently implements
//! only the *discovery* layer, not the bundle/publish pipeline.
//!
//! A subdirectory under `~<user>/cairn/` is a **valid source repo**
//! when all of:
//!
//! 1. It contains a `.cairn.yaml` file.
//! 2. The sidecar parses cleanly via [`super::sidecar::load`].
//! 3. The sidecar's `owner` field matches the local hostname
//!    (case-insensitive).
//! 4. It contains a `.git/` directory (source envoys must have a real
//!    git repo — receivers have no `.git/` because of the hybrid
//!    layout).
//! 5. The canonical path stays inside `<user_home>/cairn/` (no
//!    symlink escape).
//! 6. The directory name passes
//!    [`super::path_guard::validate_project_name`].
//!
//! Any directory failing (1)–(5) or containing an invalid sidecar is
//! counted as an error and emitted via [`SourceError`] for eventual
//! reporting as the `cairn.source_errors` trait. Benign skips
//! (sidecar absent, owner is someone else) are counted separately
//! and do NOT surface to operators.

use std::path::{Path, PathBuf};
use tracing::{debug, info, warn};

use super::path_guard::{self, PathError};
use super::sidecar::{self, SidecarConfig, SidecarError};

/// A validated cairn source repo discovered during a scan.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Source {
    /// Full swarm content-seed label:
    /// `cairn:<hostname>:<owning_user>:<project>`. See
    /// [`super::content_label`] for the construction rules.
    pub label: String,
    /// Lowercased hostname segment, retained so the publisher can
    /// derive the matching meta label without re-parsing `label`.
    pub hostname: String,
    /// Project name segment (the directory name under
    /// `~<user>/cairn/`).
    pub project: String,
    /// Canonicalized path to the source directory.
    pub source_path: PathBuf,
    /// The owning Unix user (derived from the local user whose home
    /// contains this repo, not from the sidecar's `owner` field).
    pub owning_user: String,
    /// Unix uid of the owning user, for privilege dropping during
    /// `git bundle create`.
    pub owning_uid: u32,
    /// Unix gid of the owning user.
    pub owning_gid: u32,
    /// Validated sidecar contents.
    pub sidecar: SidecarConfig,
}

/// A directory that was scanned but rejected, with a human-readable
/// reason. Callers should surface these via the `cairn.source_errors`
/// trait so operators can see why their repo isn't syncing without
/// having to grep logs.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SourceError {
    pub path: PathBuf,
    pub reason: String,
}

/// Outcome of a full scan pass.
#[derive(Debug, Default)]
pub struct ScanResult {
    /// Every valid source repo found.
    pub sources: Vec<Source>,
    /// Errors that should be surfaced to operators.
    pub errors: Vec<SourceError>,
    /// Count of directories skipped for benign reasons (sidecar
    /// absent, owner is someone else, local `.git/` present with a
    /// different owner — operator's own clone). Not an error.
    pub skip_count: usize,
}

/// Scan every local user's `~/cairn/*` and classify each
/// subdirectory. Pure function — no I/O beyond filesystem reads,
/// no state mutation, no side effects.
///
/// `hostname` is the local envoy's hostname, used for the
/// case-insensitive owner match against the sidecar.
///
/// `list_local_users` returns `(username, home_path)` for each real
/// user with a home directory on this envoy. Injected for
/// testability. Production callers should pass
/// [`crate::echo::scanner::list_home_dirs`].
pub fn scan<F>(hostname: &str, list_local_users: F) -> ScanResult
where
    F: Fn() -> Vec<(String, PathBuf)>,
{
    let mut result = ScanResult::default();
    let hostname_lc = hostname.trim().to_lowercase();
    if hostname_lc.is_empty() {
        warn!("cairn: empty hostname, cannot scan sources");
        return result;
    }

    for (user, home) in list_local_users() {
        let cairn_dir = home.join("cairn");
        if !cairn_dir.is_dir() {
            continue;
        }
        let entries = match std::fs::read_dir(&cairn_dir) {
            Ok(e) => e,
            Err(e) => {
                result.errors.push(SourceError {
                    path: cairn_dir.clone(),
                    reason: format!("cannot read cairn directory: {}", e),
                });
                continue;
            }
        };
        for entry in entries.flatten() {
            let path = entry.path();
            if !path.is_dir() {
                continue;
            }
            classify(&path, &user, &home, &hostname_lc, &mut result);
        }
    }

    result
}

/// Production convenience wrapper that reads local users from
/// [`crate::echo::scanner::list_home_dirs`].
pub fn scan_local(hostname: &str) -> ScanResult {
    scan(hostname, crate::echo::scanner::list_home_dirs)
}

fn classify(path: &Path, user: &str, home: &Path, hostname_lc: &str, result: &mut ScanResult) {
    let sidecar_path = path.join(".cairn.yaml");

    let sidecar_conf = match sidecar::load(&sidecar_path) {
        Ok(c) => c,
        Err(SidecarError::NotFound) => {
            // Directory exists but is not cairn-managed. Benign.
            result.skip_count += 1;
            return;
        }
        Err(e) => {
            result.errors.push(SourceError {
                path: sidecar_path,
                reason: format!("invalid sidecar: {}", e),
            });
            return;
        }
    };

    if sidecar_conf.owner != hostname_lc {
        debug!(
            path = %path.display(),
            owner = %sidecar_conf.owner,
            hostname = %hostname_lc,
            "cairn: not source owner for this repo, leaving alone"
        );
        result.skip_count += 1;
        return;
    }

    // Source envoy must have a real .git/ directory.
    let git_dir = path.join(".git");
    if !git_dir.is_dir() {
        result.errors.push(SourceError {
            path: path.to_path_buf(),
            reason:
                "sidecar owner matches self but .git/ is missing; source envoy must hold a real git repo"
                    .to_string(),
        });
        return;
    }

    // Canonicalize + containment check.
    let canon = match path_guard::check_source_path(path, home) {
        Ok(p) => p,
        Err(PathError::Escape { path: p, root }) => {
            result.errors.push(SourceError {
                path: path.to_path_buf(),
                reason: format!("path {} escapes cairn root {}", p, root),
            });
            return;
        }
        Err(e) => {
            result.errors.push(SourceError {
                path: path.to_path_buf(),
                reason: format!("path canonicalization failed: {}", e),
            });
            return;
        }
    };

    let project = match canon.file_name().and_then(|n| n.to_str()) {
        Some(n) => n.to_string(),
        None => {
            result.errors.push(SourceError {
                path: canon.clone(),
                reason: "cannot derive project name from path".to_string(),
            });
            return;
        }
    };
    if let Err(e) = path_guard::validate_project_name(&project) {
        result.errors.push(SourceError {
            path: canon.clone(),
            reason: format!("invalid project name: {}", e),
        });
        return;
    }

    let (owning_uid, owning_gid) = match dir_owner(&canon) {
        Some(pair) => pair,
        None => {
            result.errors.push(SourceError {
                path: canon.clone(),
                reason: "cannot stat directory to determine Unix owner".to_string(),
            });
            return;
        }
    };

    // Sanity check: the directory owner should match the local user
    // whose home contains it. Mismatch is a warning, not fatal —
    // operators may have weird ownership setups, and the publish
    // pipeline uses the *actual* directory owner for privilege
    // dropping rather than the declared user.
    if let Some(info) = crate::platform::users::lookup_user(user) {
        if info.uid != owning_uid {
            warn!(
                path = %canon.display(),
                dir_uid = owning_uid,
                expected_uid = info.uid,
                user = user,
                "cairn: directory owner UID does not match home directory's user; \
                 will use the directory's actual owner for privilege dropping"
            );
        }
    }

    let label = super::content_label(hostname_lc, user, &project);

    if sidecar_conf.paused {
        info!(
            label = %label,
            "cairn: source registered as paused; no new bundles will be published until unpaused"
        );
    } else {
        debug!(label = %label, "cairn: source registered");
    }

    result.sources.push(Source {
        label,
        hostname: hostname_lc.to_string(),
        project,
        source_path: canon,
        owning_user: user.to_string(),
        owning_uid,
        owning_gid,
        sidecar: sidecar_conf,
    });
}

#[cfg(unix)]
fn dir_owner(path: &Path) -> Option<(u32, u32)> {
    use std::os::unix::fs::MetadataExt;
    let meta = std::fs::metadata(path).ok()?;
    Some((meta.uid(), meta.gid()))
}

#[cfg(not(unix))]
fn dir_owner(_path: &Path) -> Option<(u32, u32)> {
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::tempdir;

    fn write_sidecar(dir: &Path, owner: &str) {
        write_sidecar_with(dir, owner, false);
    }

    fn write_sidecar_with(dir: &Path, owner: &str, paused: bool) {
        fs::write(
            dir.join(".cairn.yaml"),
            format!(
                "owner: {}\nmax_mirrors: 5\ntags: []\npaused: {}\ndescription: \"\"\n",
                owner, paused
            ),
        )
        .unwrap();
    }

    fn users_list(user: &str, home: PathBuf) -> impl Fn() -> Vec<(String, PathBuf)> {
        let user = user.to_string();
        move || vec![(user.clone(), home.clone())]
    }

    #[test]
    fn scans_missing_cairn_dir() {
        let home = tempdir().unwrap();
        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert!(result.sources.is_empty());
        assert!(result.errors.is_empty());
    }

    #[test]
    fn scans_empty_cairn_dir() {
        let home = tempdir().unwrap();
        fs::create_dir_all(home.path().join("cairn")).unwrap();

        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert!(result.sources.is_empty());
        assert!(result.errors.is_empty());
    }

    #[test]
    fn discovers_valid_source() {
        let home = tempdir().unwrap();
        let project = home.path().join("cairn").join("orangutan");
        fs::create_dir_all(project.join(".git")).unwrap();
        write_sidecar(&project, "danlap");

        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert_eq!(result.sources.len(), 1, "expected one valid source");
        let source = &result.sources[0];
        assert_eq!(source.label, "cairn:danlap:dan:orangutan");
        assert_eq!(source.owning_user, "dan");
        assert!(!source.sidecar.paused);
        assert!(result.errors.is_empty());
    }

    #[test]
    fn skips_non_owner_repo() {
        let home = tempdir().unwrap();
        let project = home.path().join("cairn").join("orangutan");
        fs::create_dir_all(project.join(".git")).unwrap();
        write_sidecar(&project, "homeserver");

        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert!(result.sources.is_empty());
        assert!(result.errors.is_empty());
        assert_eq!(result.skip_count, 1);
    }

    #[test]
    fn skips_repo_without_sidecar() {
        let home = tempdir().unwrap();
        let project = home.path().join("cairn").join("orangutan");
        fs::create_dir_all(project.join(".git")).unwrap();

        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert!(result.sources.is_empty());
        assert!(result.errors.is_empty());
        assert_eq!(result.skip_count, 1);
    }

    #[test]
    fn errors_on_owner_match_without_git_dir() {
        let home = tempdir().unwrap();
        let project = home.path().join("cairn").join("orangutan");
        fs::create_dir_all(&project).unwrap();
        write_sidecar(&project, "danlap");

        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert!(result.sources.is_empty());
        assert_eq!(result.errors.len(), 1);
        assert!(result.errors[0].reason.contains(".git"));
    }

    #[test]
    fn errors_on_invalid_sidecar() {
        let home = tempdir().unwrap();
        let project = home.path().join("cairn").join("orangutan");
        fs::create_dir_all(project.join(".git")).unwrap();
        fs::write(
            project.join(".cairn.yaml"),
            "not_valid: true\nowner:\nmax_mirrors: abc\n",
        )
        .unwrap();

        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert!(result.sources.is_empty());
        assert_eq!(result.errors.len(), 1);
        assert!(result.errors[0].reason.contains("invalid sidecar"));
    }

    #[test]
    fn discovers_paused_source() {
        let home = tempdir().unwrap();
        let project = home.path().join("cairn").join("orangutan");
        fs::create_dir_all(project.join(".git")).unwrap();
        write_sidecar_with(&project, "danlap", true);

        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert_eq!(result.sources.len(), 1);
        assert!(result.sources[0].sidecar.paused);
    }

    #[test]
    fn discovers_multiple_sources() {
        let home = tempdir().unwrap();
        for name in &["orangutan", "dotfiles", "notes"] {
            let project = home.path().join("cairn").join(name);
            fs::create_dir_all(project.join(".git")).unwrap();
            write_sidecar(&project, "danlap");
        }

        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert_eq!(result.sources.len(), 3);
        let labels: Vec<&str> = result.sources.iter().map(|s| s.label.as_str()).collect();
        assert!(labels.contains(&"cairn:danlap:dan:orangutan"));
        assert!(labels.contains(&"cairn:danlap:dan:dotfiles"));
        assert!(labels.contains(&"cairn:danlap:dan:notes"));
    }

    #[test]
    fn ignores_non_directory_entries() {
        let home = tempdir().unwrap();
        let cairn = home.path().join("cairn");
        fs::create_dir_all(&cairn).unwrap();
        fs::write(cairn.join("stray_file"), b"nope").unwrap();

        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert!(result.sources.is_empty());
        assert!(result.errors.is_empty());
    }

    #[test]
    fn hostname_match_is_case_insensitive() {
        let home = tempdir().unwrap();
        let project = home.path().join("cairn").join("orangutan");
        fs::create_dir_all(project.join(".git")).unwrap();
        write_sidecar(&project, "DANLAP");

        let result = scan("DanLap", users_list("dan", home.path().to_path_buf()));
        assert_eq!(result.sources.len(), 1);
    }

    #[test]
    fn empty_hostname_returns_empty_scan() {
        let home = tempdir().unwrap();
        let project = home.path().join("cairn").join("orangutan");
        fs::create_dir_all(project.join(".git")).unwrap();
        write_sidecar(&project, "danlap");

        let result = scan("", users_list("dan", home.path().to_path_buf()));
        assert!(result.sources.is_empty());
        assert!(result.errors.is_empty());
    }

    #[test]
    fn multiple_users_discovered_independently() {
        let dan_home = tempdir().unwrap();
        let bob_home = tempdir().unwrap();

        let dan_proj = dan_home.path().join("cairn").join("orangutan");
        fs::create_dir_all(dan_proj.join(".git")).unwrap();
        write_sidecar(&dan_proj, "danlap");

        let bob_proj = bob_home.path().join("cairn").join("notes");
        fs::create_dir_all(bob_proj.join(".git")).unwrap();
        write_sidecar(&bob_proj, "danlap");

        let dan_home_path = dan_home.path().to_path_buf();
        let bob_home_path = bob_home.path().to_path_buf();
        let result = scan("danlap", move || {
            vec![
                ("dan".to_string(), dan_home_path.clone()),
                ("bob".to_string(), bob_home_path.clone()),
            ]
        });

        assert_eq!(result.sources.len(), 2);
        let labels: Vec<&str> = result.sources.iter().map(|s| s.label.as_str()).collect();
        assert!(labels.contains(&"cairn:danlap:dan:orangutan"));
        assert!(labels.contains(&"cairn:danlap:bob:notes"));
    }

    #[test]
    fn error_aggregates_do_not_short_circuit_scan() {
        let home = tempdir().unwrap();

        // Good repo.
        let good = home.path().join("cairn").join("good");
        fs::create_dir_all(good.join(".git")).unwrap();
        write_sidecar(&good, "danlap");

        // Bad repo — sidecar owner matches but no .git/.
        let bad = home.path().join("cairn").join("bad");
        fs::create_dir_all(&bad).unwrap();
        write_sidecar(&bad, "danlap");

        let result = scan("danlap", users_list("dan", home.path().to_path_buf()));
        assert_eq!(result.sources.len(), 1);
        assert_eq!(result.sources[0].label, "cairn:danlap:dan:good");
        assert_eq!(result.errors.len(), 1);
        assert!(result.errors[0].path.ends_with("bad"));
    }
}
