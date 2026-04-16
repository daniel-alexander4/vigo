// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Source-side bundle creation + content-seed swarm publishing.
//!
//! For each non-paused [`Source`] discovered by [`super::source::scan`],
//! [`publish_source`] produces a `git bundle` of the source repo and
//! drops it as a content seed into
//! [`crate::platform::paths::default_seeds_dir`]. From there, the
//! existing swarm machinery takes over:
//!
//! 1. The `swarm_seeds` trait collector (`agent/src/traits/swarm_seeds.rs`)
//!    discovers each new file on its next collection pass, hashes it,
//!    and symlinks it into the blob cache.
//! 2. [`crate::swarm::register_seeds_in_manifest`] (called from the
//!    check-in loop) inserts the entry into the swarm manifest CRDT,
//!    which gossips it to the rest of the fleet.
//!
//! Vigorepo content seeds are flagged claim-only in the manifest
//! (label prefix `vigorepo:`), so the standard self-assignment path
//! in [`crate::swarm::manifest::Manifest::assignments_for`] explicitly
//! skips them. Receivers discover which seeds to download via LAN
//! multicast claim announcements ([`super::pending_claims`]) and
//! cross-subnet `extra_mirrors` direct push.
//!
//! Vigorepo never calls a swarm "publish" API directly — it just
//! writes seed files. This keeps the publisher pure I/O and avoids
//! coupling vigorepo to the in-memory `swarm::Manifest` handle.
//!
//! ### Single-seed layout
//!
//! Every successful publish produces exactly one seed:
//!
//! - **Content seed** `vigorepo:<host>:<user>:<project>`
//!   — contains the git bundle.
//!
//! There is no separate meta seed. The validated sidecar fields
//! (`max_mirrors`, `tags`, `description`) flow to the server through
//! the `vigorepo` trait payload (see [`super::state::SourceTraitEntry`]),
//! so a swarm-side meta blob would be dead weight that no consumer
//! reads. Earlier revisions of vigorepo published a paired meta seed;
//! that path was removed in the receiver-precursor commit.
//!
//! The `.swarm` JSON manifest is written before the seed file
//! itself, matching the existing race-free pattern used by
//! [`server/api/swarm.go::Distribute`] — without manifest-first the
//! `swarm_seeds` trait collector could observe a payload file with
//! no companion and report it as broken.
//!
//! ### Self-healing
//!
//! State is persisted per-label as `{git_sha, blob_sha}`. On each
//! publish attempt:
//!
//! - If `git_sha == HEAD` → no work, return [`Outcome::UpToDate`].
//! - If state has a prior `blob_sha` but it is no longer Active in
//!   the local swarm manifest (retention-revoked while this envoy was
//!   offline), the prior state is dropped and the next bundle is a
//!   full bundle. The plan documents this as "self-healing on next
//!   commit."
//! - Otherwise the bundle is incremental from `<git_sha>..HEAD`.
//!
//! ### Privilege boundary
//!
//! The agent runs as root. `git bundle create` runs in a child that
//! has been [`run_as_user`][crate::platform::users::run_as_user]'d to
//! the source repo's owning Unix user, so vigorepo never reads a
//! user's git data as root. The bundle is written to the shared
//! sticky-bit tmp dir at `<vigorepo_dir>/tmp/`, then the agent (as
//! root) atomically renames it into the swarm seeds directory.

use std::fs;
use std::io::{self, Read};
use std::path::{Path, PathBuf};

use sha2::{Digest, Sha256};
use thiserror::Error;
use tracing::{info, warn};

use super::path_guard;
use super::source::Source;
use super::state::{ensure_tmp_dir, State};
use super::git_ops;

/// Maximum allowed bundle size, in bytes. Re-exports
/// [`super::MAX_BUNDLE_SIZE`] so existing code that imports
/// `publisher::MAX_BUNDLE_BYTES` keeps compiling — the single source
/// of truth for all vigorepo hard caps is `mod.rs`.
pub const MAX_BUNDLE_BYTES: u64 = super::MAX_BUNDLE_SIZE;

/// Retention applied to both the meta and content seeds. Bundles are
/// re-published on every commit, so 7 days comfortably covers any
/// reasonable rescan/reactor cadence.
pub const SEED_RETENTION: &str = "7d";

/// Target group filter for vigorepo seeds. The hard cap on materialized
/// mirrors is enforced agent-side via the LAN selector and receiver
/// caps, not via the swarm `target` filter; from the swarm's
/// perspective vigorepo seeds are open to any envoy.
pub const SEED_TARGET: &str = "*";

/// Read-only context for a publish pass. Built once per scan from
/// platform defaults via [`PublishContext::from_defaults`]; tests
/// construct it pointing at tempdirs.
#[derive(Debug, Clone)]
pub struct PublishContext {
    /// `/var/lib/vigo/vigorepo/` — owns `state.json` and `tmp/`.
    pub state_dir: PathBuf,
    /// `/var/lib/vigo/swarm/seed/` — destination for both meta and
    /// content seed files.
    pub seeds_dir: PathBuf,
    /// `/var/lib/vigo/swarm/manifest.json` — read-only for the
    /// self-healing prior-blob existence check.
    pub manifest_path: PathBuf,
    /// Retention string applied to the `.swarm` manifest of every
    /// seed vigorepo writes.
    pub retention: String,
    /// Hard upper bound on bundle size in bytes.
    pub max_bundle_bytes: u64,
}

impl PublishContext {
    /// Resolve a context from the agent's standard platform paths.
    pub fn from_defaults() -> Self {
        let state_dir = crate::platform::paths::default_vigorepo_dir();
        let seeds_dir = crate::platform::paths::default_seeds_dir();
        let blobs_dir = crate::platform::paths::default_blobs_dir();
        // The manifest sits at the swarm root (parent of .blobs/).
        // This mirrors `swarm::manifest::Manifest::load`'s layout.
        let manifest_path = blobs_dir
            .parent()
            .map(|p| p.join("manifest.json"))
            .unwrap_or_else(|| PathBuf::from("/var/lib/vigo/swarm/manifest.json"));
        Self {
            state_dir,
            seeds_dir,
            manifest_path,
            retention: SEED_RETENTION.to_string(),
            max_bundle_bytes: MAX_BUNDLE_BYTES,
        }
    }
}

/// Outcome of one publish attempt for a single source.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Outcome {
    /// `paused: true` in the sidecar — nothing published, existing
    /// seeds will retention-expire naturally.
    Paused { label: String },
    /// HEAD matches the prior `git_sha` in state — already published.
    UpToDate { label: String },
    /// New bundle was published. `full` is true on the first publish
    /// for a label or after a self-healing reset.
    Published {
        label: String,
        git_sha: String,
        blob_sha: String,
        bundle_size: u64,
        full: bool,
    },
}

impl Outcome {
    pub fn label(&self) -> &str {
        match self {
            Outcome::Paused { label }
            | Outcome::UpToDate { label }
            | Outcome::Published { label, .. } => label,
        }
    }
}

/// Errors that can occur during a publish attempt for a single
/// source. All errors are scoped to one label; a failure on one
/// source must not abort the rest of the scan.
#[derive(Debug, Error)]
pub enum PublishError {
    #[error("source path re-check failed for {label}: {reason}")]
    PathReCheck { label: String, reason: String },

    #[error("git rev-parse HEAD failed for {label}: {reason}")]
    HeadParse { label: String, reason: String },

    #[error("git bundle create failed for {label}: {reason}")]
    BundleCreate { label: String, reason: String },

    #[error("bundle for {label} is {size} bytes, exceeds {limit} byte cap")]
    OversizedBundle {
        label: String,
        size: u64,
        limit: u64,
    },

    #[error("I/O error for {label}: {source}")]
    Io {
        label: String,
        #[source]
        source: io::Error,
    },
}

/// Publish a single source. Pure function over its arguments — does
/// no global I/O beyond what `ctx` and `source` describe.
pub fn publish_source(
    source: &Source,
    state: &mut State,
    ctx: &PublishContext,
) -> Result<Outcome, PublishError> {
    if source.sidecar.paused {
        return Ok(Outcome::Paused {
            label: source.label.clone(),
        });
    }

    // TOCTOU re-check: confirm the source path still lives inside
    // the user's `~/vigorepo/` after the discovery pass. The owning
    // user's home is looked up live in case the user's record changed
    // between discovery and publish.
    let user_home = match crate::platform::users::user_home(&source.owning_user) {
        Some(h) => h,
        None => {
            return Err(PublishError::PathReCheck {
                label: source.label.clone(),
                reason: format!("cannot resolve home for user {}", source.owning_user),
            });
        }
    };
    if let Err(e) = path_guard::check_source_path(&source.source_path, &user_home) {
        return Err(PublishError::PathReCheck {
            label: source.label.clone(),
            reason: e.to_string(),
        });
    }

    // Resolve current git HEAD (as user, never as root).
    let head_sha = git_rev_parse_head(source)?;

    // Self-healing: drop prior state if our previously-published blob
    // is no longer Active in the local manifest. If state.json is
    // ahead of HEAD via the same git_sha, declare up-to-date.
    if let Some(prior) = state.labels.get(&source.label).cloned() {
        if prior.git_sha == head_sha {
            return Ok(Outcome::UpToDate {
                label: source.label.clone(),
            });
        }
        if !blob_active_in_manifest(&ctx.manifest_path, &prior.blob_sha) {
            warn!(
                label = %source.label,
                prior_blob = %&prior.blob_sha[..12.min(prior.blob_sha.len())],
                "vigorepo: prior bundle no longer active in local manifest; restarting from full bundle"
            );
            state.forget(&source.label);
        }
    }

    let prior_git_sha = state.labels.get(&source.label).map(|s| s.git_sha.clone());
    let full = prior_git_sha.is_none();
    let rev_spec = match &prior_git_sha {
        Some(sha) => format!("{}..HEAD", sha),
        None => "HEAD".to_string(),
    };

    let tmp_dir = ensure_tmp_dir(&ctx.state_dir).map_err(|e| PublishError::Io {
        label: source.label.clone(),
        source: e,
    })?;
    let bundle_path = tmp_dir.join(format!("{}.bundle", source.label));

    // Best-effort cleanup of any leftover bundle from a prior crash.
    let _ = fs::remove_file(&bundle_path);

    let mut cmd = git_ops::build_command(git_ops::RunAs::User {
        uid: source.owning_uid,
        gid: source.owning_gid,
    });
    cmd.arg("-C")
        .arg(&source.source_path)
        .arg("bundle")
        .arg("create")
        .arg(&bundle_path)
        .arg(&rev_spec);
    let output = cmd.output().map_err(|e| PublishError::BundleCreate {
        label: source.label.clone(),
        reason: format!("spawn: {}", e),
    })?;
    if !output.status.success() {
        let _ = fs::remove_file(&bundle_path);
        return Err(PublishError::BundleCreate {
            label: source.label.clone(),
            reason: format!(
                "exit {}: {}",
                output.status,
                String::from_utf8_lossy(&output.stderr).trim()
            ),
        });
    }

    let size = fs::metadata(&bundle_path)
        .map_err(|e| PublishError::Io {
            label: source.label.clone(),
            source: e,
        })?
        .len();
    if size > ctx.max_bundle_bytes {
        let _ = fs::remove_file(&bundle_path);
        return Err(PublishError::OversizedBundle {
            label: source.label.clone(),
            size,
            limit: ctx.max_bundle_bytes,
        });
    }

    let blob_sha = hash_file(&bundle_path).map_err(|e| PublishError::Io {
        label: source.label.clone(),
        source: e,
    })?;

    // Publish the content seed: rename the bundle from tmp into the
    // seeds dir. There is no separate "meta seed" — the validated
    // sidecar fields flow to the server through the `vigorepo` trait
    // payload (see `state::SourceTraitEntry`), so a swarm-side meta
    // seed would be dead weight. The trait route was locked in at
    // commit `c692d86e`; receivers discover content seeds via LAN
    // multicast claims and cross-subnet direct push.
    write_seed_from_path(&ctx.seeds_dir, &source.label, &bundle_path, &ctx.retention).map_err(
        |e| PublishError::Io {
            label: source.label.clone(),
            source: e,
        },
    )?;

    state.record(&source.label, &head_sha, &blob_sha);
    state.save(&ctx.state_dir).map_err(|e| PublishError::Io {
        label: source.label.clone(),
        source: e,
    })?;

    info!(
        label = %source.label,
        git_sha = %&head_sha[..12.min(head_sha.len())],
        blob_sha = %&blob_sha[..12.min(blob_sha.len())],
        size,
        full,
        "vigorepo: published bundle"
    );

    Ok(Outcome::Published {
        label: source.label.clone(),
        git_sha: head_sha,
        blob_sha,
        bundle_size: size,
        full,
    })
}

fn git_rev_parse_head(source: &Source) -> Result<String, PublishError> {
    let mut cmd = git_ops::build_command(git_ops::RunAs::User {
        uid: source.owning_uid,
        gid: source.owning_gid,
    });
    cmd.arg("-C")
        .arg(&source.source_path)
        .arg("rev-parse")
        .arg("HEAD");
    let output = cmd.output().map_err(|e| PublishError::HeadParse {
        label: source.label.clone(),
        reason: format!("spawn: {}", e),
    })?;
    if !output.status.success() {
        return Err(PublishError::HeadParse {
            label: source.label.clone(),
            reason: format!(
                "exit {}: {}",
                output.status,
                String::from_utf8_lossy(&output.stderr).trim()
            ),
        });
    }
    let sha = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if !is_valid_sha1(&sha) {
        return Err(PublishError::HeadParse {
            label: source.label.clone(),
            reason: format!("unexpected output: {:?}", sha),
        });
    }
    Ok(sha)
}

fn is_valid_sha1(s: &str) -> bool {
    s.len() == 40 && s.chars().all(|c| c.is_ascii_hexdigit())
}

/// Read `<manifest_path>` (the swarm CRDT's persisted JSON) and
/// return whether the given blob sha is currently `Active`. Returns
/// `false` on any error (missing file, parse failure, sha not present
/// or revoked) — callers treat any non-positive answer as "drop the
/// state and re-bundle from scratch."
fn blob_active_in_manifest(manifest_path: &Path, blob_sha: &str) -> bool {
    let bytes = match fs::read(manifest_path) {
        Ok(b) => b,
        Err(_) => return false,
    };
    let v: serde_json::Value = match serde_json::from_slice(&bytes) {
        Ok(v) => v,
        Err(_) => return false,
    };
    let entry = match v.get("entries").and_then(|e| e.get(blob_sha)) {
        Some(e) => e,
        None => return false,
    };
    matches!(entry.get("status").and_then(|s| s.as_str()), Some("active"))
}

fn hash_file(path: &Path) -> io::Result<String> {
    let mut f = fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = vec![0u8; 1 << 20]; // 1 MiB
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hex_encode(&hasher.finalize()))
}

fn hex_encode(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push_str(&format!("{:02x}", b));
    }
    s
}

fn swarm_manifest_json(target: &str, retention: &str) -> String {
    // Match the format written by `server/api/swarm.go::Distribute`
    // and read by `agent/src/swarm/mod.rs::register_seeds_in_manifest`.
    format!(
        "{{\"target\":{},\"retention\":{}}}",
        json_string(target),
        json_string(retention)
    )
}

fn json_string(s: &str) -> String {
    // Conservative JSON-string escaper for the small strings we use
    // here (target = "*", retention = "7d"). Avoids pulling in serde
    // to format two field values.
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// Atomic seed publishing: write the `.swarm` manifest first, then
/// move the existing bundle file (already on disk in the vigorepo
/// `tmp/` directory) into the seeds directory via rename. The bundle
/// file is moved rather than copied so a multi-GB bundle does not
/// get rewritten. Falls back to copy + delete on EXDEV.
///
/// Manifest-before-payload ordering matches the pattern in
/// `server/api/swarm.go::Distribute` and prevents the swarm trait
/// collector from observing a seed file before its `.swarm`
/// companion exists (which would otherwise be reported as a broken
/// seed).
fn write_seed_from_path(
    seeds_dir: &Path,
    label: &str,
    payload_path: &Path,
    retention: &str,
) -> io::Result<()> {
    fs::create_dir_all(seeds_dir)?;

    let manifest_path = seeds_dir.join(format!("{}.swarm", label));
    fs::write(&manifest_path, swarm_manifest_json(SEED_TARGET, retention))?;

    let final_path = seeds_dir.join(label);
    match fs::rename(payload_path, &final_path) {
        Ok(()) => Ok(()),
        Err(e) => {
            // Defensive cross-device fallback: if the tmp dir and
            // seeds dir end up on different filesystems (e.g. a
            // host that puts `/var/lib/vigo/swarm` on a separate
            // mount), `rename(2)` returns `EXDEV` and we copy +
            // delete instead. On non-unix targets we just propagate
            // the error since vigorepo only runs on unix at runtime.
            #[cfg(unix)]
            let cross_device = e.raw_os_error() == Some(libc::EXDEV);
            #[cfg(not(unix))]
            let cross_device = false;
            if cross_device {
                fs::copy(payload_path, &final_path)?;
                fs::remove_file(payload_path)?;
                Ok(())
            } else {
                Err(e)
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vigorepo::sidecar::SidecarConfig;
    use crate::vigorepo::source::Source;
    use std::path::PathBuf;
    use tempfile::tempdir;

    fn synthetic_source(label: &str, source_path: PathBuf, paused: bool) -> Source {
        Source {
            label: label.to_string(),
            hostname: "danlap".to_string(),
            project: "orangutan".to_string(),
            source_path,
            owning_user: "dan".to_string(),
            owning_uid: 1000,
            owning_gid: 1000,
            sidecar: SidecarConfig {
                owner: "danlap".to_string(),
                max_mirrors: 5,
                tags: vec![],
                paused,
                description: String::new(),
                include_envoys: vec![],
                exclude_envoys: vec![],
                when: String::new(),
                extra_mirrors: vec![],
            },
        }
    }

    fn ctx_at(dir: &Path) -> PublishContext {
        PublishContext {
            state_dir: dir.join("vigorepo"),
            seeds_dir: dir.join("seeds"),
            manifest_path: dir.join("manifest.json"),
            retention: SEED_RETENTION.to_string(),
            max_bundle_bytes: MAX_BUNDLE_BYTES,
        }
    }

    #[test]
    fn paused_source_returns_paused_outcome() {
        let dir = tempdir().unwrap();
        let mut state = State::default();
        let src = synthetic_source(
            "vigorepo:danlap:dan:orangutan",
            dir.path().join("repo"),
            true,
        );
        let ctx = ctx_at(dir.path());
        let outcome = publish_source(&src, &mut state, &ctx).unwrap();
        assert!(matches!(outcome, Outcome::Paused { .. }));
        // Paused must not write any state.
        assert!(state.labels.is_empty());
        // Paused must not touch the seeds dir.
        assert!(!ctx.seeds_dir.exists());
    }

    #[test]
    fn outcome_label_accessor_returns_label_for_each_variant() {
        let p = Outcome::Paused {
            label: "a".to_string(),
        };
        let u = Outcome::UpToDate {
            label: "b".to_string(),
        };
        let pub_ = Outcome::Published {
            label: "c".to_string(),
            git_sha: "abc".to_string(),
            blob_sha: "def".to_string(),
            bundle_size: 0,
            full: true,
        };
        assert_eq!(p.label(), "a");
        assert_eq!(u.label(), "b");
        assert_eq!(pub_.label(), "c");
    }

    #[test]
    fn is_valid_sha1_accepts_lowercase_hex_40() {
        assert!(is_valid_sha1("0123456789abcdef0123456789abcdef01234567"));
    }

    #[test]
    fn is_valid_sha1_rejects_short_long_or_nonhex() {
        assert!(!is_valid_sha1(""));
        assert!(!is_valid_sha1("abc"));
        assert!(!is_valid_sha1(&"a".repeat(41)));
        assert!(!is_valid_sha1("0123456789abcdef0123456789abcdef0123456g"));
    }

    #[test]
    fn hex_encode_known_vector() {
        assert_eq!(hex_encode(&[0x00, 0xff, 0x10]), "00ff10");
    }

    #[test]
    fn hash_file_matches_known_sha256_of_empty_file() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("empty.bin");
        fs::write(&p, b"").unwrap();
        let sha = hash_file(&p).unwrap();
        // sha256("") = e3b0c44298fc1c149afbf4c8996fb924...
        assert_eq!(
            sha,
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
    }

    #[test]
    fn swarm_manifest_json_format_matches_distribute() {
        let json = swarm_manifest_json("*", "7d");
        assert_eq!(json, r#"{"target":"*","retention":"7d"}"#);
    }

    #[test]
    fn json_string_escapes_quotes_and_backslashes() {
        assert_eq!(json_string("a\"b"), r#""a\"b""#);
        assert_eq!(json_string("c\\d"), r#""c\\d""#);
        assert_eq!(json_string("e\nf"), r#""e\nf""#);
    }

    #[test]
    fn write_seed_from_path_moves_file_into_seeds_dir() {
        let dir = tempdir().unwrap();
        let seeds = dir.path().join("seeds");
        let bundle = dir.path().join("input.bundle");
        fs::write(&bundle, b"BUNDLE-BYTES").unwrap();

        write_seed_from_path(&seeds, "vigorepo:h:u:p", &bundle, "7d").unwrap();

        assert!(!bundle.exists(), "source bundle should have been moved");
        let final_seed = seeds.join("vigorepo:h:u:p");
        assert_eq!(fs::read(&final_seed).unwrap(), b"BUNDLE-BYTES");
        assert!(seeds.join("vigorepo:h:u:p.swarm").exists());
    }

    #[test]
    fn blob_active_in_manifest_returns_false_for_missing_file() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("manifest.json");
        assert!(!blob_active_in_manifest(&p, "deadbeef"));
    }

    #[test]
    fn blob_active_in_manifest_returns_true_for_active_entry() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("manifest.json");
        let json = r#"{"version":1,"entries":{"abc123":{"status":"active"}}}"#;
        fs::write(&p, json).unwrap();
        assert!(blob_active_in_manifest(&p, "abc123"));
    }

    #[test]
    fn blob_active_in_manifest_returns_false_for_revoked_entry() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("manifest.json");
        let json = r#"{"version":1,"entries":{"abc123":{"status":"revoked"}}}"#;
        fs::write(&p, json).unwrap();
        assert!(!blob_active_in_manifest(&p, "abc123"));
    }

    #[test]
    fn blob_active_in_manifest_returns_false_for_unknown_blob() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("manifest.json");
        let json = r#"{"version":1,"entries":{"other":{"status":"active"}}}"#;
        fs::write(&p, json).unwrap();
        assert!(!blob_active_in_manifest(&p, "abc123"));
    }

    #[test]
    fn blob_active_in_manifest_handles_corrupt_json() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("manifest.json");
        fs::write(&p, b"not json").unwrap();
        assert!(!blob_active_in_manifest(&p, "abc123"));
    }

    #[test]
    fn publish_context_from_defaults_resolves_manifest_next_to_blobs() {
        let ctx = PublishContext::from_defaults();
        // The manifest sibling-of-blobs convention should at least
        // produce a path under the same root as the seeds dir.
        assert!(ctx.manifest_path.ends_with("manifest.json"));
        assert_eq!(ctx.retention, SEED_RETENTION);
        assert_eq!(ctx.max_bundle_bytes, MAX_BUNDLE_BYTES);
    }

    // Note: end-to-end publish_source() test (with real `git bundle
    // create`) is intentionally omitted from the unit suite. The host
    // git on Dan's workstation is below the MVP floor of 2.35, which
    // would cause the test to fail spuriously. Manual e2e is the
    // verification path documented in the in-progress checkpoint.
}
