// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Platform-specific default paths for the Vigo agent.
//!
//! # Environment variable overrides (test/dev back door)
//!
//! Every `default_*_dir()` helper checks a matching environment variable
//! before returning its hard-coded path. If set to a non-empty value, the
//! env var wins and is used verbatim. If unset or empty, the helper falls
//! back to the platform default. The knobs:
//!
//! - `VIGO_DATA_DIR`      → [`default_data_dir`]
//! - `VIGO_STATE_DIR`     → [`default_state_dir`]
//! - `VIGO_SNAPSHOT_DIR`  → [`default_snapshot_dir`]
//! - `VIGO_FLAGS_DIR`     → [`default_flags_dir`]
//! - `VIGO_BLOBS_DIR`     → [`default_blobs_dir`]
//! - `VIGO_SEEDS_DIR`     → [`default_seeds_dir`]
//! - `VIGO_GRAVEYARD_DIR` → [`default_graveyard_dir`]
//! - `VIGO_LONGDRAWER_DIR`  → [`default_longdrawer_dir`]
//! - `VIGO_EXECUTORS_DIR` → [`default_executors_dir`]
//!
//! These are NOT part of the user-facing configuration surface. They
//! exist so the integration test harness can relocate agent state into
//! tempdirs without shipping a `--data-dir` CLI flag (which was removed
//! because it was a half-wired footgun — see commit history). Do not
//! document these in user-facing docs. Each env var overrides exactly
//! one path; there is no implicit composition.

use std::path::PathBuf;

/// Shared env-var override check. Returns `Some(path)` if `var` is set to
/// a non-empty value, `None` otherwise. Kept tiny and allocation-light
/// since every `default_*_dir()` call runs through it on every call.
fn env_override(var: &str) -> Option<PathBuf> {
    std::env::var_os(var)
        .filter(|s| !s.is_empty())
        .map(PathBuf::from)
}

/// Default agent data directory.
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_data_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_DATA_DIR") { return p; }
    PathBuf::from("/etc/vigo-envoy")
}

#[cfg(target_os = "macos")]
pub fn default_data_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_DATA_DIR") { return p; }
    PathBuf::from("/usr/local/etc/vigo-envoy")
}

#[cfg(windows)]
pub fn default_data_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_DATA_DIR") { return p; }
    let base = std::env::var("ProgramData").unwrap_or_else(|_| r"C:\ProgramData".to_string());
    PathBuf::from(base).join("Vigo")
}

/// Default persistent state directory (LMDB).
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_state_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_STATE_DIR") { return p; }
    PathBuf::from("/var/lib/vigo/state")
}

#[cfg(target_os = "macos")]
pub fn default_state_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_STATE_DIR") { return p; }
    PathBuf::from("/Library/Vigo/state")
}

#[cfg(windows)]
pub fn default_state_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_STATE_DIR") { return p; }
    default_data_dir().join("state")
}

/// Default file snapshot directory.
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_snapshot_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_SNAPSHOT_DIR") { return p; }
    PathBuf::from("/var/lib/vigo/snapshots")
}

#[cfg(target_os = "macos")]
pub fn default_snapshot_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_SNAPSHOT_DIR") { return p; }
    PathBuf::from("/Library/Vigo/snapshots")
}

#[cfg(windows)]
pub fn default_snapshot_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_SNAPSHOT_DIR") { return p; }
    default_data_dir().join("snapshots")
}

/// Default flags directory (for `flag_set()` builtin).
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_flags_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_FLAGS_DIR") { return p; }
    PathBuf::from("/var/lib/vigo/flags")
}

#[cfg(target_os = "macos")]
pub fn default_flags_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_FLAGS_DIR") { return p; }
    PathBuf::from("/Library/Vigo/flags")
}

#[cfg(windows)]
pub fn default_flags_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_FLAGS_DIR") { return p; }
    default_data_dir().join("flags")
}

/// Default blob cache directory for swarm downloads.
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_blobs_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_BLOBS_DIR") { return p; }
    PathBuf::from("/var/lib/vigo/swarm/.blobs")
}

#[cfg(target_os = "macos")]
pub fn default_blobs_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_BLOBS_DIR") { return p; }
    PathBuf::from("/Library/Vigo/swarm/.blobs")
}

#[cfg(windows)]
pub fn default_blobs_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_BLOBS_DIR") { return p; }
    default_data_dir().join("swarm").join(".blobs")
}

/// Canonical swarm-tree root: the parent directory of [`default_blobs_dir`].
/// On Linux this is `/var/lib/vigo/swarm`; macOS `/Library/Vigo/swarm`;
/// Windows `<data>/swarm`. Honors `VIGO_BLOBS_DIR` via the underlying
/// [`default_blobs_dir`] — operators who override the blob cache get a
/// consistent swarm-tree root automatically.
///
/// Used by every site that needs to derive a sibling of the blob cache
/// (peers.json, manifest.json, seeds dir, gitback mirrors, etc.) so the
/// parent-of-blobs derivation lives in exactly one place.
pub fn default_swarm_base() -> PathBuf {
    default_blobs_dir()
        .parent()
        .expect("default_blobs_dir always has a multi-segment path")
        .to_path_buf()
}

/// Sibling `files/` directory for a parametrized blob cache root. Used by
/// the by-name symlink layer (`BlobCache::link_by_name`) and the cleanup
/// passes that sweep dangling symlinks. The argument is the blob cache dir
/// — typically [`default_blobs_dir`], but tests and the curator accelerator
/// pass a parametrized path, so this helper takes `&Path` rather than
/// reading the default itself.
///
/// `blobs_dir.parent().unwrap_or(blobs_dir)` matches the historic behavior
/// at every call site: if the blob cache happens to be at a single-segment
/// path (only possible in pathological test setups), we fall back to the
/// blobs dir itself rather than panicking.
pub fn files_dir_for(blobs_dir: &std::path::Path) -> PathBuf {
    blobs_dir.parent().unwrap_or(blobs_dir).join("files")
}

/// Manifest directory for a parametrized blob cache root: the parent of the
/// blob cache, where `manifest.json` lives. Used by the swarm_cache trait
/// collector to load the manifest for revoked-blob filtering.
pub fn manifest_dir_for(blobs_dir: &std::path::Path) -> PathBuf {
    blobs_dir.parent().unwrap_or(blobs_dir).to_path_buf()
}

/// Default seed directory for envoy-originated swarm distribution. The
/// per-subsystem env var wins; otherwise falls back to a sibling of the
/// blob cache so `VIGO_BLOBS_DIR` cascades to the whole swarm tree.
pub fn default_seeds_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_SEEDS_DIR") { return p; }
    default_swarm_base().join("seed")
}

/// Default graveyard directory: revoked blobs awaiting hard-delete after the
/// 7-day grace window. Within the window, a same-SHA reseed can resurrect.
/// Cascade: `VIGO_GRAVEYARD_DIR` → `VIGO_BLOBS_DIR`-derived sibling.
pub fn default_graveyard_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_GRAVEYARD_DIR") { return p; }
    default_swarm_base().join("graveyard")
}

/// Default longdrawer state directory (per-user snapshots + tombstones).
/// Cascade: `VIGO_LONGDRAWER_DIR` → `VIGO_BLOBS_DIR`-derived sibling.
pub fn default_longdrawer_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_LONGDRAWER_DIR") { return p; }
    default_swarm_base().join("longdrawer")
}

/// Default gitback state directory (last_seeded_sha, bundle tmp dir).
/// Cascade: `VIGO_GITBACK_DIR` → `VIGO_BLOBS_DIR`-derived sibling.
pub fn default_gitback_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_GITBACK_DIR") { return p; }
    default_swarm_base().join("gitback")
}

/// Default custom executors directory.
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_executors_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_EXECUTORS_DIR") { return p; }
    PathBuf::from("/etc/vigo-envoy/executors")
}

#[cfg(target_os = "macos")]
pub fn default_executors_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_EXECUTORS_DIR") { return p; }
    PathBuf::from("/usr/local/etc/vigo-envoy/executors")
}

#[cfg(windows)]
pub fn default_executors_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_EXECUTORS_DIR") { return p; }
    default_data_dir().join("executors")
}

/// Default binary install path.
#[cfg(unix)]
pub fn default_binary_path() -> PathBuf {
    PathBuf::from("/usr/local/sbin/vigo")
}

#[cfg(windows)]
pub fn default_binary_path() -> PathBuf {
    let base = std::env::var("ProgramFiles").unwrap_or_else(|_| r"C:\Program Files".to_string());
    PathBuf::from(base).join("Vigo").join("vigo.exe")
}

/// System hosts file path.
#[cfg(unix)]
pub fn hosts_file() -> PathBuf {
    PathBuf::from("/etc/hosts")
}

#[cfg(windows)]
pub fn hosts_file() -> PathBuf {
    let sysroot = std::env::var("SystemRoot").unwrap_or_else(|_| r"C:\Windows".to_string());
    PathBuf::from(sysroot)
        .join("System32")
        .join("drivers")
        .join("etc")
        .join("hosts")
}

/// System SSH known_hosts file path.
#[cfg(unix)]
pub fn ssh_known_hosts() -> PathBuf {
    PathBuf::from("/etc/ssh/ssh_known_hosts")
}

#[cfg(windows)]
pub fn ssh_known_hosts() -> PathBuf {
    let base = std::env::var("ProgramData").unwrap_or_else(|_| r"C:\ProgramData".to_string());
    PathBuf::from(base).join("ssh").join("ssh_known_hosts")
}

/// Shell command and args for executing a string command.
/// Returns (program, arg_to_pass_command).
#[cfg(unix)]
pub fn shell_command() -> (&'static str, &'static str) {
    ("/bin/sh", "-c")
}

#[cfg(windows)]
pub fn shell_command() -> (&'static str, &'static str) {
    ("cmd.exe", "/C")
}

/// Service name for the agent.
pub const SERVICE_NAME: &str = "vigo-envoy";

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;

    /// Env vars are process-global. Every test that mutates one must hold
    /// this lock to avoid races with parallel tests reading the same vars.
    static ENV_LOCK: Mutex<()> = Mutex::new(());

    #[test]
    fn paths_are_non_empty() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        // Clear any leftover override from another test run in the same
        // process so we get the real hard-coded defaults.
        for var in [
            "VIGO_DATA_DIR", "VIGO_STATE_DIR", "VIGO_SNAPSHOT_DIR",
            "VIGO_FLAGS_DIR", "VIGO_BLOBS_DIR", "VIGO_SEEDS_DIR",
            "VIGO_GRAVEYARD_DIR", "VIGO_REJECTED_DIR", "VIGO_LONGDRAWER_DIR",
            "VIGO_EXECUTORS_DIR",
        ] {
            std::env::remove_var(var);
        }
        assert!(!default_data_dir().as_os_str().is_empty());
        assert!(!default_state_dir().as_os_str().is_empty());
        assert!(!default_snapshot_dir().as_os_str().is_empty());
        assert!(!default_flags_dir().as_os_str().is_empty());
        assert!(!default_executors_dir().as_os_str().is_empty());
        assert!(!default_binary_path().as_os_str().is_empty());
        assert!(!hosts_file().as_os_str().is_empty());
        assert!(!ssh_known_hosts().as_os_str().is_empty());
    }

    #[test]
    fn default_swarm_base_matches_blobs_parent() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("VIGO_BLOBS_DIR");
        // The helper's invariant: it returns exactly the parent of
        // default_blobs_dir(). Pinning it here means a future change to
        // default_blobs_dir() that makes the path single-segment
        // (breaking the .parent() invariant) fails at `cargo test` rather
        // than panicking at agent startup.
        assert_eq!(
            default_swarm_base(),
            default_blobs_dir().parent().unwrap(),
        );
    }

    #[test]
    fn default_swarm_base_honors_env_override() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::set_var("VIGO_BLOBS_DIR", "/tmp/test-override/blobs");
        assert_eq!(
            default_swarm_base(),
            PathBuf::from("/tmp/test-override"),
        );
        std::env::remove_var("VIGO_BLOBS_DIR");
    }

    // The four subordinate swarm-tree helpers (seeds, graveyard, longdrawer,
    // gitback) each accept their own `VIGO_<SUB>_DIR` override first, then
    // fall back to a sibling of `default_swarm_base()` so a `VIGO_BLOBS_DIR`
    // override cascades to the whole swarm tree. One test per subsystem;
    // each pins all 3 states (default → cascade → specific-wins) so a future
    // copy-paste mistake in one helper can't go unnoticed.

    #[test]
    fn default_seeds_dir_cascades_then_specific_wins() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("VIGO_BLOBS_DIR");
        std::env::remove_var("VIGO_SEEDS_DIR");
        assert!(default_seeds_dir().ends_with("seed"));

        std::env::set_var("VIGO_BLOBS_DIR", "/tmp/vigo-cascade-seeds/blobs");
        assert_eq!(default_seeds_dir(), PathBuf::from("/tmp/vigo-cascade-seeds/seed"));

        std::env::set_var("VIGO_SEEDS_DIR", "/tmp/vigo-specific-seeds");
        assert_eq!(default_seeds_dir(), PathBuf::from("/tmp/vigo-specific-seeds"));

        std::env::remove_var("VIGO_BLOBS_DIR");
        std::env::remove_var("VIGO_SEEDS_DIR");
    }

    #[test]
    fn default_graveyard_dir_cascades_then_specific_wins() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("VIGO_BLOBS_DIR");
        std::env::remove_var("VIGO_GRAVEYARD_DIR");
        assert!(default_graveyard_dir().ends_with("graveyard"));

        std::env::set_var("VIGO_BLOBS_DIR", "/tmp/vigo-cascade-graveyard/blobs");
        assert_eq!(default_graveyard_dir(), PathBuf::from("/tmp/vigo-cascade-graveyard/graveyard"));

        std::env::set_var("VIGO_GRAVEYARD_DIR", "/tmp/vigo-specific-graveyard");
        assert_eq!(default_graveyard_dir(), PathBuf::from("/tmp/vigo-specific-graveyard"));

        std::env::remove_var("VIGO_BLOBS_DIR");
        std::env::remove_var("VIGO_GRAVEYARD_DIR");
    }

    #[test]
    fn default_longdrawer_dir_cascades_then_specific_wins() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("VIGO_BLOBS_DIR");
        std::env::remove_var("VIGO_LONGDRAWER_DIR");
        assert!(default_longdrawer_dir().ends_with("longdrawer"));

        std::env::set_var("VIGO_BLOBS_DIR", "/tmp/vigo-cascade-longdrawer/blobs");
        assert_eq!(default_longdrawer_dir(), PathBuf::from("/tmp/vigo-cascade-longdrawer/longdrawer"));

        std::env::set_var("VIGO_LONGDRAWER_DIR", "/tmp/vigo-specific-longdrawer");
        assert_eq!(default_longdrawer_dir(), PathBuf::from("/tmp/vigo-specific-longdrawer"));

        std::env::remove_var("VIGO_BLOBS_DIR");
        std::env::remove_var("VIGO_LONGDRAWER_DIR");
    }

    #[test]
    fn default_gitback_dir_cascades_then_specific_wins() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("VIGO_BLOBS_DIR");
        std::env::remove_var("VIGO_GITBACK_DIR");
        assert!(default_gitback_dir().ends_with("gitback"));

        std::env::set_var("VIGO_BLOBS_DIR", "/tmp/vigo-cascade-gitback/blobs");
        assert_eq!(default_gitback_dir(), PathBuf::from("/tmp/vigo-cascade-gitback/gitback"));

        std::env::set_var("VIGO_GITBACK_DIR", "/tmp/vigo-specific-gitback");
        assert_eq!(default_gitback_dir(), PathBuf::from("/tmp/vigo-specific-gitback"));

        std::env::remove_var("VIGO_BLOBS_DIR");
        std::env::remove_var("VIGO_GITBACK_DIR");
    }

    #[test]
    fn files_dir_for_is_sibling_of_blobs() {
        // Real shape: blob cache at `/var/lib/vigo/swarm/.blobs` produces
        // files at `/var/lib/vigo/swarm/files`.
        let got = files_dir_for(std::path::Path::new("/var/lib/vigo/swarm/.blobs"));
        assert_eq!(got, PathBuf::from("/var/lib/vigo/swarm/files"));
    }

    #[test]
    fn files_dir_for_falls_back_when_blobs_has_no_parent() {
        // Single-segment input — `.parent()` returns Some("") on Unix,
        // None at the root. The helper's `unwrap_or(blobs_dir)` guard
        // means we never panic; we get a `files/` under the input.
        let got = files_dir_for(std::path::Path::new("blobs"));
        // The parent of a single-segment relative path is "", so the join
        // produces "files". For root-like input we'd get the blobs-dir
        // fallback. Either way: non-empty and ends with "files".
        assert!(got.ends_with("files"), "got {got:?}");
    }

    #[test]
    fn manifest_dir_for_is_parent_of_blobs() {
        let got = manifest_dir_for(std::path::Path::new("/var/lib/vigo/swarm/.blobs"));
        assert_eq!(got, PathBuf::from("/var/lib/vigo/swarm"));
    }

    #[test]
    fn shell_command_returns_pair() {
        let (prog, arg) = shell_command();
        assert!(!prog.is_empty());
        assert!(!arg.is_empty());
    }

    #[test]
    fn env_override_returns_none_for_unset() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("VIGO_TEST_UNSET_VAR_XYZ");
        assert!(env_override("VIGO_TEST_UNSET_VAR_XYZ").is_none());
    }

    #[test]
    fn env_override_returns_none_for_empty_string() {
        // Empty string is treated as "unset" because an accidental
        // VIGO_STATE_DIR="" must not nuke the state dir to ".".
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::set_var("VIGO_TEST_EMPTY_VAR_XYZ", "");
        let got = env_override("VIGO_TEST_EMPTY_VAR_XYZ");
        std::env::remove_var("VIGO_TEST_EMPTY_VAR_XYZ");
        assert!(got.is_none());
    }

    #[test]
    fn state_dir_honors_env_override() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::set_var("VIGO_STATE_DIR", "/tmp/vigo-paths-test/state");
        let got = default_state_dir();
        std::env::remove_var("VIGO_STATE_DIR");
        assert_eq!(got, PathBuf::from("/tmp/vigo-paths-test/state"));
    }

    #[test]
    fn blobs_dir_honors_env_override() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::set_var("VIGO_BLOBS_DIR", "/tmp/vigo-paths-test/blobs");
        let got = default_blobs_dir();
        std::env::remove_var("VIGO_BLOBS_DIR");
        assert_eq!(got, PathBuf::from("/tmp/vigo-paths-test/blobs"));
    }

    #[test]
    fn data_dir_env_does_not_leak_into_state_dir() {
        // Each override is scoped to its own helper — VIGO_DATA_DIR must
        // NOT implicitly move the state dir. That's exactly the
        // "one knob moves ten paths" trap --data-dir was removed to
        // avoid.
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("VIGO_STATE_DIR");
        std::env::set_var("VIGO_DATA_DIR", "/tmp/vigo-paths-test/data");
        let state = default_state_dir();
        let data = default_data_dir();
        std::env::remove_var("VIGO_DATA_DIR");
        assert_eq!(data, PathBuf::from("/tmp/vigo-paths-test/data"));
        assert_ne!(state, PathBuf::from("/tmp/vigo-paths-test/data/state"));
    }
}
