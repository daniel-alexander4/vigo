// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! SSH authorized_keys verification and ephemeral key management for Scrier.
//!
//! When the server sends `ssh_verify_pubkey` in TunnelOpen, the agent:
//!   1. Enumerates every regular user (uid != 0) on this host via libc
//!      getpwent — covers Linux `/home/*`, macOS `/Users/*`, BSDs, illumos,
//!      and LDAP/NSS users without per-OS path branching.
//!   2. Scans each user's `~/.ssh/authorized_keys` for the verify pubkey.
//!   3. Installs the ephemeral pubkey into the matched user's file (with the
//!      EPHEMERAL_MARKER comment so cleanup and convergence-strip can
//!      recognise it later).
//!   4. Returns that user's name so the server can SSH in as them.
//!
//! Root is deliberately never a scrier landing target — operators land in a
//! regular user and sudo when they need elevation. If no regular user holds
//! the verify pubkey, the session fails with a clear error.

use std::ffi::CStr;
use std::fs;
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use tracing::{info, warn};

/// A single passwd entry, narrowed to the fields scrier cares about.
struct RegularUser {
    name: String,
    home: PathBuf,
}

/// Result of checking authorized_keys for a public key.
pub struct AuthResult {
    /// The Unix username whose authorized_keys contained the verify pubkey.
    pub username: String,
    /// Path to the authorized_keys file where the ephemeral key was installed.
    pub authorized_keys_path: PathBuf,
}

/// RAII guard that removes the ephemeral key from authorized_keys on drop.
/// Ensures cleanup even on panic or early return.
pub struct EphemeralKeyGuard {
    path: Option<PathBuf>,
}

impl EphemeralKeyGuard {
    pub fn new(path: PathBuf) -> Self {
        Self { path: Some(path) }
    }

    pub fn empty() -> Self {
        Self { path: None }
    }
}

impl Drop for EphemeralKeyGuard {
    fn drop(&mut self) {
        if let Some(ref path) = self.path {
            remove_ephemeral_key(path);
        }
    }
}

/// Marker comment appended to ephemeral keys so we can identify and clean them up.
/// Public so the user executor can preserve these lines during convergence.
pub const EPHEMERAL_MARKER: &str = "# vigo-scrier-ephemeral";

/// Enumerate every regular user (uid != 0) on this host with a non-empty home
/// directory. Sorted by username for deterministic tie-break in the multi-
/// match case. Single source of truth for "every local user's authorized_keys"
/// — both `scan_and_install` and `cleanup_stale_keys` use this list.
///
/// Walks libc's passwd database (getpwent/setpwent/endpwent) so /etc/passwd,
/// LDAP/NSS, and any platform-specific home layout (Linux /home/*,
/// macOS /Users/*, BSDs, illumos) are all covered without per-OS branching.
fn regular_users() -> Vec<RegularUser> {
    let mut out: Vec<RegularUser> = Vec::new();
    // SAFETY: setpwent/getpwent/endpwent are POSIX. getpwent returns a pointer
    // to a static buffer reused on each call; we copy the fields out before
    // the next iteration. The whole sequence runs single-threaded — scrier
    // session setup is per-request, no parallel scans.
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
            out.push(RegularUser { name, home });
        }
        libc::endpwent();
    }
    out.sort_by(|a, b| a.name.cmp(&b.name));
    out
}

/// Scan every regular user's authorized_keys for the verify pubkey and, on
/// first match, install the ephemeral key in that user's file. Returns None
/// if no regular user holds the verify pubkey.
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

    let users = regular_users();
    let mut matches: Vec<(String, PathBuf)> = Vec::new();

    for u in &users {
        let ak = u.home.join(".ssh/authorized_keys");
        if !ak.exists() || is_symlink(&ak) {
            continue;
        }
        if file_contains_key(&ak, &verify_key) {
            matches.push((u.name.clone(), ak));
        }
    }

    if matches.is_empty() {
        warn!("scrier: verify pubkey not found in any regular user's authorized_keys");
        return None;
    }
    if matches.len() > 1 {
        let names: Vec<&str> = matches.iter().map(|(n, _)| n.as_str()).collect();
        warn!(
            users = ?names,
            "scrier: verify pubkey matches multiple users; landing in first (alphabetical)"
        );
    }

    let (username, ak) = matches.into_iter().next().unwrap();

    if let Err(e) = append_ephemeral_key(&ak, ephemeral_pubkey) {
        warn!(error = %e, path = %ak.display(), "scrier: failed to install ephemeral key");
        return None;
    }

    info!(username = %username, path = %ak.display(), "scrier: ephemeral key installed");
    Some(AuthResult {
        username,
        authorized_keys_path: ak,
    })
}

/// Remove any stale ephemeral keys left behind by crashed sessions. Called
/// on agent startup to clean up after unclean shutdowns. Iterates the same
/// regular-user set as `scan_and_install` so the two functions can never
/// disagree on which files they look at.
pub fn cleanup_stale_keys() {
    let users = regular_users();
    for u in &users {
        let ak = u.home.join(".ssh/authorized_keys");
        if !ak.exists() || is_symlink(&ak) {
            continue;
        }
        let content = match fs::read_to_string(&ak) {
            Ok(c) => c,
            Err(_) => continue,
        };
        if content.contains(EPHEMERAL_MARKER) {
            info!(path = %ak.display(), "scrier: removing stale ephemeral key from previous session");
            remove_ephemeral_key(&ak);
        }
    }
}

/// Remove the ephemeral key from an authorized_keys file.
pub fn remove_ephemeral_key(path: &Path) {
    let content = match fs::read_to_string(path) {
        Ok(c) => c,
        Err(e) => {
            warn!(error = %e, path = %path.display(), "failed to read authorized_keys for cleanup");
            return;
        }
    };

    if !content.contains(EPHEMERAL_MARKER) {
        return;
    }

    let filtered: Vec<&str> = content
        .lines()
        .filter(|line| !line.contains(EPHEMERAL_MARKER))
        .collect();

    // Write to a temp file then rename for atomicity.
    let tmp = path.with_extension("tmp-scrier");
    match fs::write(&tmp, filtered.join("\n") + "\n") {
        Ok(()) => {
            if let Err(e) = fs::rename(&tmp, path) {
                warn!(error = %e, path = %path.display(), "failed to rename tmp authorized_keys");
                let _ = fs::remove_file(&tmp);
            } else {
                info!(path = %path.display(), "scrier: ephemeral key removed");
            }
        }
        Err(e) => {
            warn!(error = %e, path = %path.display(), "failed to write cleaned authorized_keys");
            let _ = fs::remove_file(&tmp);
        }
    }
}

/// Check if a path is a symlink.
fn is_symlink(path: &Path) -> bool {
    match fs::symlink_metadata(path) {
        Ok(meta) => meta.file_type().is_symlink(),
        Err(_) => false,
    }
}

/// Check if an authorized_keys file contains the given normalized key.
/// Compares only the key-type + base64 portion, ignoring comments.
fn file_contains_key(path: &Path, normalized_key: &str) -> bool {
    let file = match fs::File::open(path) {
        Ok(f) => f,
        Err(_) => return false,
    };

    let reader = BufReader::new(file);
    for line in reader.lines() {
        let line = match line {
            Ok(l) => l,
            Err(_) => continue,
        };
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if normalize_key(line) == *normalized_key {
            return true;
        }
    }
    false
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

/// Append the ephemeral pubkey to an authorized_keys file with a marker comment.
fn append_ephemeral_key(path: &Path, ephemeral_pubkey: &str) -> std::io::Result<()> {
    let mut file = fs::OpenOptions::new().append(true).open(path)?;
    let key = ephemeral_pubkey.trim();
    writeln!(file, "{} {}", key, EPHEMERAL_MARKER)?;
    Ok(())
}
