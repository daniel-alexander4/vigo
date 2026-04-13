// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Cross-platform user and group primitives: name lookup, home-directory
//! resolution, and best-effort ownership assignment.

use std::path::{Path, PathBuf};

/// User information.
#[derive(Debug, Clone)]
pub struct UserInfo {
    pub uid: u32,
    pub gid: u32,
    pub name: String,
}

/// Group information.
#[derive(Debug, Clone)]
pub struct GroupInfo {
    pub gid: u32,
    pub name: String,
}

/// Look up a user by name. Returns None if not found.
#[cfg(unix)]
pub fn lookup_user(name: &str) -> Option<UserInfo> {
    nix::unistd::User::from_name(name).ok().flatten().map(|u| UserInfo {
        uid: u.uid.as_raw(),
        gid: u.gid.as_raw(),
        name: u.name,
    })
}

#[cfg(windows)]
pub fn lookup_user(name: &str) -> Option<UserInfo> {
    // On Windows, check if user exists via `net user`.
    let output = std::process::Command::new("net")
        .args(["user", name])
        .output()
        .ok()?;
    if output.status.success() {
        Some(UserInfo {
            uid: 0,
            gid: 0,
            name: name.to_string(),
        })
    } else {
        None
    }
}

/// Look up a group by name. Returns None if not found.
#[cfg(unix)]
pub fn lookup_group(name: &str) -> Option<GroupInfo> {
    nix::unistd::Group::from_name(name).ok().flatten().map(|g| GroupInfo {
        gid: g.gid.as_raw(),
        name: g.name,
    })
}

#[cfg(windows)]
pub fn lookup_group(name: &str) -> Option<GroupInfo> {
    let output = std::process::Command::new("net")
        .args(["localgroup", name])
        .output()
        .ok()?;
    if output.status.success() {
        Some(GroupInfo {
            gid: 0,
            name: name.to_string(),
        })
    } else {
        None
    }
}

/// Get the current user's UID (0 on Windows).
#[cfg(unix)]
pub fn current_uid() -> u32 {
    nix::unistd::getuid().as_raw()
}

#[cfg(windows)]
pub fn current_uid() -> u32 {
    0
}

/// Get the current user's GID (0 on Windows).
#[cfg(unix)]
pub fn current_gid() -> u32 {
    nix::unistd::getgid().as_raw()
}

#[cfg(windows)]
pub fn current_gid() -> u32 {
    0
}

/// Resolve a username's home directory.
#[cfg(unix)]
pub fn user_home(username: &str) -> Option<PathBuf> {
    nix::unistd::User::from_name(username)
        .ok()
        .flatten()
        .map(|u| u.dir)
}

#[cfg(windows)]
pub fn user_home(username: &str) -> Option<PathBuf> {
    let p = PathBuf::from(format!(r"C:\Users\{}", username));
    if p.is_dir() {
        Some(p)
    } else {
        None
    }
}

/// Set file ownership to a named user. Best-effort: if the user doesn't exist
/// or the caller isn't root, silently no-ops. Callers that need to know
/// whether the chown succeeded should resolve `lookup_user` and call
/// `nix::unistd::chown` directly.
#[cfg(unix)]
pub fn chown_to_user(path: &Path, username: &str) {
    use nix::unistd::{chown, Gid, Uid};
    if let Some(info) = lookup_user(username) {
        let _ = chown(
            path,
            Some(Uid::from_raw(info.uid)),
            Some(Gid::from_raw(info.gid)),
        );
    }
}

#[cfg(windows)]
pub fn chown_to_user(_path: &Path, _username: &str) {
    // Windows ownership model is ACL-based; vigosync/vigogit don't set
    // per-file ACLs on Windows — leave ownership to the default inheritance.
}

/// Configure a `Command` to run as a specific user on Unix systems.
///
/// Uses `CommandExt::uid()` / `gid()` to drop privileges before `exec()`.
/// Clears the inherited environment and re-injects a minimal hardened
/// set: `HOME`, `USER`, `PATH`, plus `GIT_CONFIG_GLOBAL=/dev/null`,
/// `GIT_CONFIG_SYSTEM=/dev/null`, and `GIT_TERMINAL_PROMPT=0` to
/// neutralize git configuration attack surface.
///
/// The caller must already have sufficient privilege to uid-swap
/// (typically: running as root). Fork+exec is handled safely by the
/// std library via `posix_spawn`, avoiding async-signal-safety
/// concerns a manual closure-based fork would introduce under a
/// tokio runtime.
///
/// Returns `&mut Command` for chaining: the caller can continue
/// configuring the command after the privilege drop is applied.
#[cfg(unix)]
pub fn run_as_user(
    cmd: &mut std::process::Command,
    uid: u32,
    gid: u32,
) -> &mut std::process::Command {
    use std::os::unix::process::CommandExt;

    let (username, home) = nix::unistd::User::from_uid(nix::unistd::Uid::from_raw(uid))
        .ok()
        .flatten()
        .map(|u| (u.name, u.dir))
        .unwrap_or_else(|| ("vigogit".to_string(), PathBuf::from("/")));

    cmd.uid(uid)
        .gid(gid)
        .env_clear()
        .env("HOME", home)
        .env("USER", username)
        .env("PATH", "/usr/bin:/bin")
        .env("GIT_CONFIG_GLOBAL", "/dev/null")
        .env("GIT_CONFIG_SYSTEM", "/dev/null")
        .env("GIT_TERMINAL_PROMPT", "0");
    cmd
}

#[cfg(windows)]
pub fn run_as_user(
    cmd: &mut std::process::Command,
    _uid: u32,
    _gid: u32,
) -> &mut std::process::Command {
    // Windows has no setuid model. Return the command unchanged; the
    // caller on Windows is responsible for choosing an appropriate
    // process identity via other means. Vigogit is Unix-only in MVP.
    cmd
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn current_uid_gid() {
        // Just verify these don't panic.
        let _uid = current_uid();
        let _gid = current_gid();
    }

    #[cfg(unix)]
    #[test]
    fn lookup_root_user() {
        let user = lookup_user("root");
        assert!(user.is_some());
        assert_eq!(user.unwrap().uid, 0);
    }

    #[cfg(unix)]
    #[test]
    fn lookup_root_group() {
        let group = lookup_group("root");
        assert!(group.is_some());
        assert_eq!(group.unwrap().gid, 0);
    }

    #[test]
    fn lookup_nonexistent_user() {
        assert!(lookup_user("nonexistent_user_xyz_12345").is_none());
    }

    #[test]
    fn lookup_nonexistent_group() {
        assert!(lookup_group("nonexistent_group_xyz_12345").is_none());
    }

    #[cfg(unix)]
    #[test]
    fn run_as_user_sets_hardening_env() {
        use std::process::Command;
        let mut cmd = Command::new("true");
        run_as_user(&mut cmd, 1000, 1000);

        let envs: Vec<(String, String)> = cmd
            .get_envs()
            .filter_map(|(k, v)| {
                v.map(|v| {
                    (
                        k.to_string_lossy().into_owned(),
                        v.to_string_lossy().into_owned(),
                    )
                })
            })
            .collect();

        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_CONFIG_GLOBAL" && v == "/dev/null"));
        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_CONFIG_SYSTEM" && v == "/dev/null"));
        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_TERMINAL_PROMPT" && v == "0"));
        assert!(envs.iter().any(|(k, _)| k == "HOME"));
        assert!(envs.iter().any(|(k, _)| k == "USER"));
        assert!(envs.iter().any(|(k, v)| k == "PATH" && v == "/usr/bin:/bin"));
    }
}
