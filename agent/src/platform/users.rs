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
    // Windows ownership model is ACL-based; longdrawer/gitback don't set
    // per-file ACLs on Windows — leave ownership to the default inheritance.
}

/// True iff `path` is a real directory (not a symlink) owned by `uid`.
/// Uses lstat (`symlink_metadata`), so a symlink at `path` returns false
/// rather than being followed. Use before root-side reads/removes of
/// files *under* a user-controlled directory — e.g. removing fixed-named
/// files under `~/.lockbox`, where a user could symlink `~/.lockbox` to
/// redirect a root delete elsewhere.
#[cfg(unix)]
pub fn is_real_dir_owned_by(path: &Path, uid: u32) -> bool {
    use std::os::unix::fs::MetadataExt;
    match std::fs::symlink_metadata(path) {
        Ok(m) => !m.file_type().is_symlink() && m.is_dir() && m.uid() == uid,
        Err(_) => false,
    }
}

#[cfg(not(unix))]
pub fn is_real_dir_owned_by(path: &Path, _uid: u32) -> bool {
    path.is_dir()
}

/// Scrub a state directory that lives under a user's home, safely against
/// a symlink-redirect: the path is attacker-plantable, so a user could
/// replace their state dir with a symlink to `/etc` (say) to trick a
/// root-driven `remove_dir_all` into deleting elsewhere. Rules:
///   - nothing there → Ok (nothing to do);
///   - a symlink (or any non-directory) → unlink just that entry, never
///     following it, so a planted link is cleaned without touching its
///     target;
///   - a real directory owned by `uid` → recursively removed;
///   - a real directory owned by someone else → refused (should never
///     happen under the user's own home; refusing is the safe default).
///
/// NOTE: this is a check-then-act guard, so a TOCTOU window remains — an
/// attacker who swaps the path between the lstat and the remove can still
/// win the race. It closes the standing exploit (a persistently-planted
/// symlink awaiting the next flag-revocation scrub); a fully race-free
/// version needs `O_NOFOLLOW` `openat` + `fstatat` on a held fd.
#[cfg(unix)]
pub fn scrub_user_dir(path: &Path, uid: u32) -> std::io::Result<()> {
    use std::os::unix::fs::MetadataExt;
    let meta = match std::fs::symlink_metadata(path) {
        Ok(m) => m,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(e),
    };
    if meta.file_type().is_symlink() || !meta.is_dir() {
        return std::fs::remove_file(path);
    }
    if meta.uid() != uid {
        return Err(std::io::Error::new(
            std::io::ErrorKind::PermissionDenied,
            format!(
                "{} is owned by uid {} not the target user's uid {} — refusing to scrub",
                path.display(),
                meta.uid(),
                uid
            ),
        ));
    }
    std::fs::remove_dir_all(path)
}

#[cfg(not(unix))]
pub fn scrub_user_dir(path: &Path, _uid: u32) -> std::io::Result<()> {
    match std::fs::symlink_metadata(path) {
        Ok(_) => std::fs::remove_dir_all(path),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(e),
    }
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
        .unwrap_or_else(|| ("vigo".to_string(), PathBuf::from("/")));

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
    // process identity via other means. Gitback is Unix-only in MVP.
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
    fn scrub_user_dir_does_not_follow_symlink() {
        use std::os::unix::fs::symlink;
        let uid = current_uid();
        let root = tempfile::tempdir().unwrap();

        // A "victim" tree outside the scrub path, with a file that must
        // survive.
        let victim = root.path().join("victim");
        std::fs::create_dir(&victim).unwrap();
        std::fs::write(victim.join("precious.txt"), b"keep me").unwrap();

        // The scrub target is a symlink an attacker planted, pointing at
        // the victim dir.
        let planted = root.path().join(".vigo-puddle");
        symlink(&victim, &planted).unwrap();

        scrub_user_dir(&planted, uid).unwrap();

        // The symlink itself is gone; the victim tree is untouched.
        assert!(std::fs::symlink_metadata(&planted).is_err(), "planted symlink not unlinked");
        assert!(victim.join("precious.txt").exists(), "scrub followed the symlink and deleted the target");
    }

    #[cfg(unix)]
    #[test]
    fn scrub_user_dir_removes_real_owned_dir_and_noops_missing() {
        let uid = current_uid();
        let root = tempfile::tempdir().unwrap();

        // Missing path → Ok, nothing to do.
        scrub_user_dir(&root.path().join("absent"), uid).unwrap();

        // Real dir owned by us → removed.
        let real = root.path().join(".lockbox");
        std::fs::create_dir(&real).unwrap();
        std::fs::write(real.join("state"), b"x").unwrap();
        scrub_user_dir(&real, uid).unwrap();
        assert!(!real.exists(), "real owned dir not removed");
    }

    #[cfg(unix)]
    #[test]
    fn is_real_dir_owned_by_rejects_symlinked_parent() {
        use std::os::unix::fs::symlink;
        let uid = current_uid();
        let root = tempfile::tempdir().unwrap();

        let real = root.path().join("realdot");
        std::fs::create_dir(&real).unwrap();
        assert!(is_real_dir_owned_by(&real, uid));

        // A symlink to a real dir is refused (lstat sees the link).
        let link = root.path().join("linkdot");
        symlink(&real, &link).unwrap();
        assert!(!is_real_dir_owned_by(&link, uid), "symlinked parent accepted");

        // Foreign uid → refused.
        assert!(!is_real_dir_owned_by(&real, uid.wrapping_add(1)));
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
