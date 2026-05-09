// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

/// Default seed directory for envoy-originated swarm distribution.
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_seeds_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_SEEDS_DIR") { return p; }
    PathBuf::from("/var/lib/vigo/swarm/seed")
}

#[cfg(target_os = "macos")]
pub fn default_seeds_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_SEEDS_DIR") { return p; }
    PathBuf::from("/Library/Vigo/swarm/seed")
}

#[cfg(windows)]
pub fn default_seeds_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_SEEDS_DIR") { return p; }
    default_data_dir().join("swarm").join("seed")
}

/// Default graveyard directory: revoked blobs awaiting hard-delete after the
/// 7-day grace window. Within the window, a same-SHA reseed can resurrect.
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_graveyard_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_GRAVEYARD_DIR") { return p; }
    PathBuf::from("/var/lib/vigo/swarm/graveyard")
}

#[cfg(target_os = "macos")]
pub fn default_graveyard_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_GRAVEYARD_DIR") { return p; }
    PathBuf::from("/Library/Vigo/swarm/graveyard")
}

#[cfg(windows)]
pub fn default_graveyard_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_GRAVEYARD_DIR") { return p; }
    default_data_dir().join("swarm").join("graveyard")
}

/// Default longdrawer state directory (per-user snapshots + tombstones).
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_longdrawer_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_LONGDRAWER_DIR") { return p; }
    PathBuf::from("/var/lib/vigo/swarm/longdrawer")
}

#[cfg(target_os = "macos")]
pub fn default_longdrawer_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_LONGDRAWER_DIR") { return p; }
    PathBuf::from("/Library/Vigo/swarm/longdrawer")
}

#[cfg(windows)]
pub fn default_longdrawer_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_LONGDRAWER_DIR") { return p; }
    default_data_dir().join("swarm").join("longdrawer")
}

/// Default swarmgit state directory (last_seeded_sha, bundle tmp dir).
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_swarmgit_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_SWARMGIT_DIR") { return p; }
    PathBuf::from("/var/lib/vigo/swarm/swarmgit")
}

#[cfg(target_os = "macos")]
pub fn default_swarmgit_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_SWARMGIT_DIR") { return p; }
    PathBuf::from("/Library/Vigo/swarm/swarmgit")
}

#[cfg(windows)]
pub fn default_swarmgit_dir() -> PathBuf {
    if let Some(p) = env_override("VIGO_SWARMGIT_DIR") { return p; }
    default_data_dir().join("swarm").join("swarmgit")
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
