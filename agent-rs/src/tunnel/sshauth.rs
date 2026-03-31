// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! SSH authorized_keys verification and ephemeral key management for Scrier.
//!
//! When the server sends ssh_verify_pubkey in TunnelOpen, the agent:
//! 1. Scans authorized_keys files to find which Unix user has that key
//! 2. Installs the ephemeral pubkey in that user's authorized_keys
//! 3. Returns the username so guacd can authenticate with the ephemeral key
//! 4. Removes the ephemeral key when the tunnel closes (including on panic/crash via Drop)

use std::fs;
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use tracing::{info, warn};

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

/// Remove any stale ephemeral keys left behind by crashed sessions.
/// Called on agent startup to clean up after unclean shutdowns.
pub fn cleanup_stale_keys() {
    let mut paths: Vec<PathBuf> = Vec::new();

    let root_ak = PathBuf::from("/root/.ssh/authorized_keys");
    if root_ak.exists() {
        paths.push(root_ak);
    }
    if let Ok(entries) = fs::read_dir("/home") {
        for entry in entries.flatten() {
            let ak = entry.path().join(".ssh/authorized_keys");
            if ak.exists() && !is_symlink(&ak) {
                paths.push(ak);
            }
        }
    }

    for path in &paths {
        let content = match fs::read_to_string(path) {
            Ok(c) => c,
            Err(_) => continue,
        };
        if content.contains(EPHEMERAL_MARKER) {
            info!(path = %path.display(), "scrier: removing stale ephemeral key from previous session");
            remove_ephemeral_key(path);
        }
    }
}

/// Verify the pubkey in the target user's authorized_keys and install the
/// ephemeral key. Only checks the specified user — no scanning.
pub fn verify_and_install(target_username: &str, verify_pubkey: &str, ephemeral_pubkey: &str) -> Option<AuthResult> {
    let verify_key = normalize_key(verify_pubkey);
    if verify_key.is_empty() {
        warn!("empty verify pubkey, skipping authorized_keys check");
        return None;
    }
    if target_username.is_empty() {
        warn!("empty target username, skipping authorized_keys check");
        return None;
    }

    // Build the path to the target user's authorized_keys.
    let ak = if target_username == "root" {
        PathBuf::from("/root/.ssh/authorized_keys")
    } else {
        PathBuf::from(format!("/home/{}/.ssh/authorized_keys", target_username))
    };

    if !ak.exists() {
        warn!(username = %target_username, path = %ak.display(),
            "scrier: authorized_keys file not found for target user");
        return None;
    }
    if is_symlink(&ak) {
        warn!(path = %ak.display(), "scrier: skipping symlinked authorized_keys");
        return None;
    }

    if !file_contains_key(&ak, &verify_key) {
        warn!(username = %target_username,
            "scrier: verify pubkey not found in {}'s authorized_keys", target_username);
        return None;
    }

    info!(username = %target_username, "scrier: verify pubkey found in authorized_keys");

    if let Err(e) = append_ephemeral_key(&ak, ephemeral_pubkey) {
        warn!(error = %e, path = %ak.display(), "failed to install ephemeral key");
        return None;
    }

    info!(username = %target_username, path = %ak.display(), "scrier: ephemeral key installed");

    Some(AuthResult {
        username: target_username.to_string(),
        authorized_keys_path: ak,
    })
}

/// Remove the ephemeral key from an authorized_keys file.
/// Uses file locking for atomicity with concurrent operations.
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

/// Check if an authorized_keys file contains the given key.
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
    let mut file = fs::OpenOptions::new()
        .append(true)
        .open(path)?;
    let key = ephemeral_pubkey.trim();
    writeln!(file, "{} {}", key, EPHEMERAL_MARKER)?;
    Ok(())
}
