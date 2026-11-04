// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! SSH authorized_keys verification and ephemeral key management for Scrier.
//!
//! When the server sends `ssh_verify_pubkey` in TunnelOpen, the agent:
//!   1. Enumerates every regular user (uid != 0) on this host via libc
//!      getpwent — covers Linux `/home/*`, macOS `/Users/*`, BSDs, illumos,
//!      and LDAP/NSS users without per-OS path branching.
//!   2. Scans each user's `~/.ssh/authorized_keys` for the verify pubkey.
//!   3. Installs the ephemeral pubkey into the matched user's file, tagged
//!      with EPHEMERAL_MARKER plus a per-session tag so this session's
//!      cleanup removes only its own key, and the startup sweep and the user
//!      executor's convergence-strip still recognise every scrier line.
//!   4. Returns that user's name so the server can SSH in as them.
//!
//! Root is deliberately never a scrier landing target — operators land in a
//! regular user and sudo when they need elevation. If no regular user holds
//! the verify pubkey, the session fails with a clear error.
//!
//! Every read and rewrite of a user's `authorized_keys` runs as root inside a
//! directory that user controls, so it goes through
//! [`crate::platform::safe_path`]: `~/.ssh` is opened no-follow and must be
//! owned by the user, the file is read no-follow, and the new content is
//! written to an exclusive no-follow temp, given the original file's owner and
//! mode on its descriptor, and `renameat`-ed into place.

use std::ffi::CStr;
use std::io;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use tracing::{info, warn};

use crate::platform::safe_path::SafeDir;

/// A single passwd entry, narrowed to the fields scrier cares about.
struct RegularUser {
    name: String,
    uid: u32,
    gid: u32,
    home: PathBuf,
}

/// Result of checking authorized_keys for a public key.
pub struct AuthResult {
    /// The Unix username whose authorized_keys contained the verify pubkey.
    pub username: String,
    /// Path to the authorized_keys file where the ephemeral key was installed.
    pub authorized_keys_path: PathBuf,
    /// Where and under which tag the key went — handed to [`EphemeralKeyGuard`].
    pub install: EphemeralInstall,
}

/// Enough to find this session's key again: the user's home + uid (to reopen
/// `~/.ssh` safely) and the per-session tag on the line.
#[derive(Clone, Debug)]
pub struct EphemeralInstall {
    home: PathBuf,
    uid: u32,
    tag: String,
}

/// RAII guard that removes this session's ephemeral key from authorized_keys
/// on drop. Ensures cleanup even on panic or early return.
pub struct EphemeralKeyGuard {
    install: Option<EphemeralInstall>,
}

impl EphemeralKeyGuard {
    pub fn new(install: EphemeralInstall) -> Self {
        Self { install: Some(install) }
    }

    pub fn empty() -> Self {
        Self { install: None }
    }
}

impl Drop for EphemeralKeyGuard {
    fn drop(&mut self) {
        if let Some(ref i) = self.install {
            remove_session_key(i);
        }
    }
}

/// Marker comment appended to ephemeral keys so we can identify and clean them up.
/// Public so the user executor can preserve these lines during convergence.
pub const EPHEMERAL_MARKER: &str = "# vigo-scrier-ephemeral";

/// authorized_keys larger than this is refused rather than read into memory.
const MAX_AUTHORIZED_KEYS_BYTES: u64 = 1024 * 1024;

const AUTHORIZED_KEYS: &str = "authorized_keys";

/// Serializes every authorized_keys read-modify-write (two tunnel sessions
/// landing in the same user would otherwise lose one key) and every
/// getpwent walk (getpwent's iterator state is process-global).
static AUTH_KEYS_LOCK: Mutex<()> = Mutex::new(());

fn lock() -> std::sync::MutexGuard<'static, ()> {
    AUTH_KEYS_LOCK.lock().unwrap_or_else(|p| p.into_inner())
}

/// Enumerate every regular user (uid != 0) on this host with a non-empty home
/// directory. Sorted by username for deterministic tie-break in the multi-
/// match case. Single source of truth for "every local user's authorized_keys"
/// — both `scan_and_install` and `cleanup_stale_keys` use this list.
///
/// Walks libc's passwd database (getpwent/setpwent/endpwent) so /etc/passwd,
/// LDAP/NSS, and any platform-specific home layout (Linux /home/*,
/// macOS /Users/*, BSDs, illumos) are all covered without per-OS branching.
/// Callers hold [`AUTH_KEYS_LOCK`].
fn regular_users() -> Vec<RegularUser> {
    let mut out: Vec<RegularUser> = Vec::new();
    // SAFETY: setpwent/getpwent/endpwent are POSIX. getpwent returns a pointer
    // to a static buffer reused on each call; we copy the fields out before
    // the next iteration. Callers hold AUTH_KEYS_LOCK, so no two walks in this
    // process interleave.
    unsafe {
        libc::setpwent();
        loop {
            let pw = libc::getpwent();
            if pw.is_null() {
                break;
            }
            let pw = &*pw;
            if pw.pw_uid == 0 {
                continue;
            }
            let name = match CStr::from_ptr(pw.pw_name).to_str() {
                Ok(s) if !s.is_empty() => s.to_string(),
                _ => continue,
            };
            let home = match CStr::from_ptr(pw.pw_dir).to_str() {
                Ok(s) if !s.is_empty() => PathBuf::from(s),
                _ => continue,
            };
            out.push(RegularUser { name, uid: pw.pw_uid, gid: pw.pw_gid, home });
        }
        libc::endpwent();
    }
    out.sort_by(|a, b| a.name.cmp(&b.name));
    out
}

/// Open `<home>/.ssh` without following a symlink and require it to be owned
/// by `uid` (or root). `home` comes from passwd — a root-controlled anchor.
fn open_ssh_dir(home: &Path, uid: u32) -> io::Result<SafeDir> {
    SafeDir::open_anchor(home)?.subdir(".ssh")?.require_owner(uid)
}

/// Read `~/.ssh/authorized_keys` safely: no-follow, regular file, owned by the
/// user or root, size-capped. `Ok(None)` when absent.
fn read_authorized_keys(ssh: &SafeDir, uid: u32) -> io::Result<Option<String>> {
    match ssh.read_capped(AUTHORIZED_KEYS, &[uid, 0], MAX_AUTHORIZED_KEYS_BYTES) {
        Ok(b) => String::from_utf8(b)
            .map(Some)
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "authorized_keys is not UTF-8")),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e),
    }
}

/// Atomically replace `~/.ssh/authorized_keys` with `content`, owned by
/// `owner` at `mode`, via an exclusive no-follow temp in the same dir fd.
fn publish_authorized_keys(ssh: &SafeDir, content: &str, owner: (u32, u32), mode: u32) -> io::Result<()> {
    let tmp = format!(".authorized_keys.vigo-{}", uuid::Uuid::new_v4().as_simple());
    ssh.write_atomic(AUTHORIZED_KEYS, &tmp, content.as_bytes(), mode, Some(owner), None)
}

/// Shared authorized_keys rewrite for scrier and the user executor.
///
/// `manage == false` (scrier): `~/.ssh` and the file must already exist as a
/// real directory / regular file owned by the user (or root); the result keeps
/// the file's existing owner and mode.
///
/// `manage == true` (user executor): `~/.ssh` is created 0700 if missing and
/// owned by `uid:gid`; a non-regular entry at the file name (a planted symlink)
/// is replaced rather than followed; the result is `uid:gid` 0600.
///
/// `edit` receives the current content and returns the new content, or `None`
/// for "no change". Returns whether the file was rewritten. Callers hold
/// [`AUTH_KEYS_LOCK`].
fn rewrite_authorized_keys(
    home: &Path,
    uid: u32,
    gid: u32,
    manage: bool,
    edit: impl FnOnce(Option<&str>) -> Option<String>,
) -> io::Result<bool> {
    let ssh = if manage {
        let anchor = SafeDir::open_anchor(home)?;
        anchor.mkdir(".ssh", 0o700)?;
        let ssh = anchor.subdir(".ssh")?.require_owner(uid)?;
        ssh.chown_self(Some(uid), Some(gid))?;
        ssh.chmod_self(0o700)?;
        ssh
    } else {
        open_ssh_dir(home, uid)?
    };
    let (existing, owner, mode) = if manage {
        let existing = match read_authorized_keys(&ssh, uid) {
            Ok(c) => c,
            // A symlink / FIFO / foreign-owned file at the name: replace it.
            Err(e) if e.kind() != io::ErrorKind::InvalidData => None,
            Err(e) => return Err(e),
        };
        (existing, (uid, gid), 0o600)
    } else {
        let existing = read_authorized_keys(&ssh, uid)?
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "no authorized_keys"))?;
        let st = ssh.stat(AUTHORIZED_KEYS)?;
        (Some(existing), (st.uid, st.gid), st.perm())
    };
    match edit(existing.as_deref()) {
        Some(new) => {
            publish_authorized_keys(&ssh, &new, owner, mode)?;
            Ok(true)
        }
        None => Ok(false),
    }
}

/// User-executor entry point: write `content` (the managed keys) to
/// `<home>/.ssh/authorized_keys`, preserving any live scrier ephemeral lines
/// already in the file, owned by `uid:gid` 0600 with `~/.ssh` 0700.
pub fn write_managed_authorized_keys(home: &Path, uid: u32, gid: u32, keys: &str) -> io::Result<()> {
    let _g = lock();
    rewrite_authorized_keys(home, uid, gid, true, |existing| {
        let mut content = keys.to_string();
        for line in existing.unwrap_or("").lines() {
            if line.contains(EPHEMERAL_MARKER) {
                if !content.is_empty() && !content.ends_with('\n') {
                    content.push('\n');
                }
                content.push_str(line);
                content.push('\n');
            }
        }
        Some(content)
    })
    .map(|_| ())
}

/// Scan every regular user's authorized_keys for the verify pubkey and, on
/// first match, install the ephemeral key in that user's file. Returns None
/// if no regular user holds the verify pubkey. Blocking (passwd/NSS walk +
/// file I/O) — call it from `spawn_blocking`.
///
/// If multiple users hold the same pubkey (operator key-reuse), the first
/// alphabetical match wins and a WARN line names every match so the
/// misconfiguration is discoverable.
pub fn scan_and_install(verify_pubkey: &str, ephemeral_pubkey: &str) -> Option<AuthResult> {
    let verify_key = normalize_key(verify_pubkey);
    if verify_key.is_empty() {
        warn!("scrier: empty verify pubkey, refusing to scan");
        return None;
    }
    let ephemeral = match valid_ephemeral_key(ephemeral_pubkey) {
        Some(k) => k,
        None => {
            warn!("scrier: ephemeral pubkey is empty or spans more than one line, refusing");
            return None;
        }
    };

    let _g = lock();
    let users = regular_users();
    let mut matches: Vec<&RegularUser> = Vec::new();

    for u in &users {
        let Ok(ssh) = open_ssh_dir(&u.home, u.uid) else { continue };
        let Ok(Some(content)) = read_authorized_keys(&ssh, u.uid) else { continue };
        if content_contains_key(&content, &verify_key) {
            matches.push(u);
        }
    }

    if matches.is_empty() {
        warn!("scrier: verify pubkey not found in any regular user's authorized_keys");
        return None;
    }
    if matches.len() > 1 {
        let names: Vec<&str> = matches.iter().map(|u| u.name.as_str()).collect();
        warn!(
            users = ?names,
            "scrier: verify pubkey matches multiple users; landing in first (alphabetical)"
        );
    }

    let u = matches[0];
    let ak = u.home.join(".ssh").join(AUTHORIZED_KEYS);
    let install = EphemeralInstall {
        home: u.home.clone(),
        uid: u.uid,
        tag: format!("session-{}", uuid::Uuid::new_v4().as_simple()),
    };

    if let Err(e) = append_ephemeral_key(&install, u.gid, ephemeral) {
        warn!(error = %e, path = %ak.display(), "scrier: failed to install ephemeral key");
        return None;
    }

    info!(username = %u.name, path = %ak.display(), "scrier: ephemeral key installed");
    Some(AuthResult {
        username: u.name.clone(),
        authorized_keys_path: ak,
        install,
    })
}

/// Remove any stale ephemeral keys left behind by crashed sessions. Called
/// on agent startup — before any tunnel can open, so every scrier line in
/// any file is stale and all of them go, whatever their session tag.
/// Iterates the same regular-user set as `scan_and_install` so the two
/// functions can never disagree on which files they look at.
pub fn cleanup_stale_keys() {
    let _g = lock();
    for u in &regular_users() {
        match rewrite_authorized_keys(&u.home, u.uid, u.gid, false, |c| strip_lines(c?, EPHEMERAL_MARKER)) {
            Ok(true) => {
                info!(user = %u.name, "scrier: removed stale ephemeral key(s) from previous session");
            }
            Ok(false) => {}
            Err(e) if e.kind() == io::ErrorKind::NotFound => {}
            Err(e) => {
                tracing::debug!(user = %u.name, error = %e, "scrier: stale-key sweep skipped user");
            }
        }
    }
}

/// Remove exactly this session's key line.
fn remove_session_key(i: &EphemeralInstall) {
    let _g = lock();
    let needle = session_marker(&i.tag);
    // gid is unused when manage == false (existing owner is preserved).
    match rewrite_authorized_keys(&i.home, i.uid, 0, false, |c| strip_lines(c?, &needle)) {
        Ok(true) => info!(home = %i.home.display(), "scrier: ephemeral key removed"),
        Ok(false) => {}
        Err(e) => {
            warn!(error = %e, home = %i.home.display(), "scrier: failed to remove ephemeral key")
        }
    }
}

/// `"<marker> <tag>"` — the suffix every line this session installs carries.
fn session_marker(tag: &str) -> String {
    format!("{} {}", EPHEMERAL_MARKER, tag)
}

/// Drop every line containing `needle`; `None` when nothing matched.
fn strip_lines(content: &str, needle: &str) -> Option<String> {
    if !content.contains(needle) {
        return None;
    }
    let kept: Vec<&str> = content.lines().filter(|l| !l.contains(needle)).collect();
    let mut out = kept.join("\n");
    if !out.is_empty() {
        out.push('\n');
    }
    Some(out)
}

/// The trimmed ephemeral key, or `None` if it is empty or contains a line
/// break / other control character. A key with an embedded newline would
/// land a second, untagged line that no cleanup ever removes.
fn valid_ephemeral_key(key: &str) -> Option<&str> {
    let k = key.trim();
    if k.is_empty() || k.chars().any(|c| c.is_control()) {
        return None;
    }
    Some(k)
}

/// Does `content` (an authorized_keys body) hold the given normalized key?
/// Compares only the key-type + base64 portion, ignoring comments.
fn content_contains_key(content: &str, normalized_key: &str) -> bool {
    content.lines().map(str::trim).any(|line| {
        !line.is_empty() && !line.starts_with('#') && normalize_key(line) == *normalized_key
    })
}

/// Normalize an SSH public key to "type base64" (strip comments/options).
fn normalize_key(key: &str) -> String {
    let key = key.trim();
    let parts: Vec<&str> = key.split_whitespace().collect();
    // Standard format: "ssh-ed25519 AAAA... comment"
    // We keep type + base64, drop the comment.
    if parts.len() >= 2 && (parts[0].starts_with("ssh-") || parts[0].starts_with("ecdsa-")) {
        return format!("{} {}", parts[0], parts[1]);
    }
    key.to_string()
}

/// Append the ephemeral pubkey, tagged with the marker + session tag, via the
/// safe rewrite (never an O_APPEND open of a path the user can swap).
fn append_ephemeral_key(i: &EphemeralInstall, gid: u32, key: &str) -> io::Result<()> {
    let line = format!("{} {}\n", key, session_marker(&i.tag));
    rewrite_authorized_keys(&i.home, i.uid, gid, false, |c| {
        let mut content = c.unwrap_or("").to_string();
        if !content.is_empty() && !content.ends_with('\n') {
            content.push('\n');
        }
        content.push_str(&line);
        Some(content)
    })
    .map(|_| ())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::{symlink, MetadataExt, PermissionsExt};

    fn me() -> (u32, u32) {
        unsafe { (libc::getuid(), libc::getgid()) }
    }

    fn home_with_keys(keys: &str) -> tempfile::TempDir {
        let t = tempfile::tempdir().unwrap();
        let ssh = t.path().join(".ssh");
        std::fs::create_dir(&ssh).unwrap();
        std::fs::write(ssh.join(AUTHORIZED_KEYS), keys).unwrap();
        std::fs::set_permissions(ssh.join(AUTHORIZED_KEYS), std::fs::Permissions::from_mode(0o640))
            .unwrap();
        t
    }

    fn install(home: &Path, tag: &str) -> EphemeralInstall {
        EphemeralInstall { home: home.to_path_buf(), uid: me().0, tag: tag.to_string() }
    }

    #[test]
    fn rewrite_refuses_symlinked_ssh_dir() {
        // ~/.ssh -> outside dir holding a file named authorized_keys: neither
        // read nor rewritten.
        let outside = tempfile::tempdir().unwrap();
        let victim = outside.path().join(AUTHORIZED_KEYS);
        std::fs::write(&victim, "root-key # vigo-scrier-ephemeral x\n").unwrap();
        let home = tempfile::tempdir().unwrap();
        symlink(outside.path(), home.path().join(".ssh")).unwrap();
        let (uid, gid) = me();
        let r = rewrite_authorized_keys(home.path(), uid, gid, false, |c| strip_lines(c?, EPHEMERAL_MARKER));
        assert!(r.is_err(), "symlinked ~/.ssh must be refused");
        assert_eq!(std::fs::read_to_string(&victim).unwrap(), "root-key # vigo-scrier-ephemeral x\n");
        assert_eq!(std::fs::read_dir(outside.path()).unwrap().count(), 1, "no temp left outside");
    }

    #[test]
    fn rewrite_refuses_symlinked_authorized_keys_and_planted_tmp() {
        let outside = tempfile::tempdir().unwrap();
        let victim = outside.path().join("victim");
        std::fs::write(&victim, "precious\n").unwrap();
        let home = tempfile::tempdir().unwrap();
        let ssh = home.path().join(".ssh");
        std::fs::create_dir(&ssh).unwrap();
        symlink(&victim, ssh.join(AUTHORIZED_KEYS)).unwrap();
        let (uid, gid) = me();
        let r = rewrite_authorized_keys(home.path(), uid, gid, false, |_| Some("x\n".into()));
        assert!(r.is_err(), "a symlinked authorized_keys must not be read or replaced");
        assert_eq!(std::fs::read_to_string(&victim).unwrap(), "precious\n");
        // The old fixed temp name, planted as a link, is irrelevant now.
        std::fs::remove_file(ssh.join(AUTHORIZED_KEYS)).unwrap();
        std::fs::write(ssh.join(AUTHORIZED_KEYS), "k # vigo-scrier-ephemeral s\n").unwrap();
        symlink(&victim, ssh.join("authorized_keys.tmp-scrier")).unwrap();
        rewrite_authorized_keys(home.path(), uid, gid, false, |c| strip_lines(c?, EPHEMERAL_MARKER))
            .unwrap();
        assert_eq!(std::fs::read_to_string(&victim).unwrap(), "precious\n");
        assert_eq!(std::fs::read_to_string(ssh.join(AUTHORIZED_KEYS)).unwrap(), "");
    }

    #[test]
    fn install_and_remove_preserve_mode_and_other_sessions() {
        let home = home_with_keys("ssh-ed25519 AAAAuser user@host\n");
        let a = install(home.path(), "session-a");
        let b = install(home.path(), "session-b");
        append_ephemeral_key(&a, me().1, "ssh-ed25519 AAAAeph-a").unwrap();
        append_ephemeral_key(&b, me().1, "ssh-ed25519 AAAAeph-b").unwrap();
        let ak = home.path().join(".ssh").join(AUTHORIZED_KEYS);
        remove_session_key(&a);
        let body = std::fs::read_to_string(&ak).unwrap();
        assert!(!body.contains("AAAAeph-a"), "session a's key removed");
        assert!(body.contains("AAAAeph-b"), "session b's key must survive a's cleanup");
        assert!(body.contains("AAAAuser"));
        let m = std::fs::metadata(&ak).unwrap();
        assert_eq!(m.permissions().mode() & 0o777, 0o640, "mode preserved");
        assert_eq!(m.uid(), me().0, "owner preserved");
        remove_session_key(&b);
        assert_eq!(std::fs::read_to_string(&ak).unwrap(), "ssh-ed25519 AAAAuser user@host\n");
    }

    #[test]
    fn ephemeral_key_with_newline_rejected() {
        assert!(valid_ephemeral_key("ssh-ed25519 AAAA\nssh-ed25519 BBBB").is_none());
        assert!(valid_ephemeral_key("ssh-ed25519 AAAA\rx").is_none());
        assert!(valid_ephemeral_key("  ").is_none());
        assert_eq!(valid_ephemeral_key(" ssh-ed25519 AAAA \n"), Some("ssh-ed25519 AAAA"));
    }

    #[test]
    fn managed_write_replaces_symlink_and_keeps_ephemeral_lines() {
        let outside = tempfile::tempdir().unwrap();
        let victim = outside.path().join("victim");
        std::fs::write(&victim, "precious\n").unwrap();
        let home = tempfile::tempdir().unwrap();
        let (uid, gid) = me();
        // No ~/.ssh yet: created 0700.
        write_managed_authorized_keys(home.path(), uid, gid, "k1\n").unwrap();
        let ssh = home.path().join(".ssh");
        assert_eq!(std::fs::metadata(&ssh).unwrap().permissions().mode() & 0o777, 0o700);
        std::fs::write(ssh.join(AUTHORIZED_KEYS), "e # vigo-scrier-ephemeral session-x\n").unwrap();
        write_managed_authorized_keys(home.path(), uid, gid, "k2\n").unwrap();
        assert_eq!(
            std::fs::read_to_string(ssh.join(AUTHORIZED_KEYS)).unwrap(),
            "k2\ne # vigo-scrier-ephemeral session-x\n"
        );
        std::fs::remove_file(ssh.join(AUTHORIZED_KEYS)).unwrap();
        symlink(&victim, ssh.join(AUTHORIZED_KEYS)).unwrap();
        write_managed_authorized_keys(home.path(), uid, gid, "k3\n").unwrap();
        assert_eq!(std::fs::read_to_string(&victim).unwrap(), "precious\n");
        assert_eq!(std::fs::read_to_string(ssh.join(AUTHORIZED_KEYS)).unwrap(), "k3\n");
        let m = std::fs::symlink_metadata(ssh.join(AUTHORIZED_KEYS)).unwrap();
        assert!(m.file_type().is_file());
        assert_eq!(m.permissions().mode() & 0o777, 0o600);
    }
}
