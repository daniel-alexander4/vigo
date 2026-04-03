// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Platform-specific default paths for the Vigo agent.

use std::path::PathBuf;

/// Default agent data directory.
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_data_dir() -> PathBuf {
    PathBuf::from("/etc/vigo-envoy")
}

#[cfg(target_os = "macos")]
pub fn default_data_dir() -> PathBuf {
    PathBuf::from("/usr/local/etc/vigo-envoy")
}

#[cfg(windows)]
pub fn default_data_dir() -> PathBuf {
    let base = std::env::var("ProgramData").unwrap_or_else(|_| r"C:\ProgramData".to_string());
    PathBuf::from(base).join("Vigo")
}

/// Default persistent state directory (LMDB).
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_state_dir() -> PathBuf {
    PathBuf::from("/var/lib/vigo/state")
}

#[cfg(target_os = "macos")]
pub fn default_state_dir() -> PathBuf {
    PathBuf::from("/Library/Vigo/state")
}

#[cfg(windows)]
pub fn default_state_dir() -> PathBuf {
    default_data_dir().join("state")
}

/// Default file snapshot directory.
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_snapshot_dir() -> PathBuf {
    PathBuf::from("/var/lib/vigo/snapshots")
}

#[cfg(target_os = "macos")]
pub fn default_snapshot_dir() -> PathBuf {
    PathBuf::from("/Library/Vigo/snapshots")
}

#[cfg(windows)]
pub fn default_snapshot_dir() -> PathBuf {
    default_data_dir().join("snapshots")
}

/// Default flags directory (for `flag_set()` builtin).
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_flags_dir() -> PathBuf {
    PathBuf::from("/var/lib/vigo/flags")
}

#[cfg(target_os = "macos")]
pub fn default_flags_dir() -> PathBuf {
    PathBuf::from("/Library/Vigo/flags")
}

#[cfg(windows)]
pub fn default_flags_dir() -> PathBuf {
    default_data_dir().join("flags")
}

/// Default blob cache directory for swarm downloads.
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_blobs_dir() -> PathBuf {
    PathBuf::from("/var/lib/vigo/blobs")
}

#[cfg(target_os = "macos")]
pub fn default_blobs_dir() -> PathBuf {
    PathBuf::from("/Library/Vigo/blobs")
}

#[cfg(windows)]
pub fn default_blobs_dir() -> PathBuf {
    default_data_dir().join("blobs")
}

/// Default custom executors directory.
#[cfg(all(unix, not(target_os = "macos")))]
pub fn default_executors_dir() -> PathBuf {
    PathBuf::from("/etc/vigo-envoy/executors")
}

#[cfg(target_os = "macos")]
pub fn default_executors_dir() -> PathBuf {
    PathBuf::from("/usr/local/etc/vigo-envoy/executors")
}

#[cfg(windows)]
pub fn default_executors_dir() -> PathBuf {
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

    #[test]
    fn paths_are_non_empty() {
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
}
