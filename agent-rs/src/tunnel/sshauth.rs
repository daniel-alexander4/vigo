// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! SSH authorized_keys verification and ephemeral key management for Scrier.
//!
//! When the server sends ssh_verify_pubkey in TunnelOpen, the agent:
//! 1. Scans authorized_keys files to find which Unix user has that key
//! 2. Installs the ephemeral pubkey in that user's authorized_keys
//! 3. Returns the username so guacd can authenticate with the ephemeral key
//! 4. Removes the ephemeral key when the tunnel closes

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

/// Marker comment appended to ephemeral keys so we can identify and clean them up.
const EPHEMERAL_MARKER: &str = "# vigo-scrier-ephemeral";

/// Check all users' authorized_keys for the verify pubkey. If found, install
/// the ephemeral pubkey and return the username.
pub fn verify_and_install(verify_pubkey: &str, ephemeral_pubkey: &str) -> Option<AuthResult> {
    let verify_key = normalize_key(verify_pubkey);
    if verify_key.is_empty() {
        warn!("empty verify pubkey, skipping authorized_keys check");
        return None;
    }

    // Scan /root and /home/* for authorized_keys files.
    let mut candidates: Vec<(String, PathBuf)> = Vec::new();

    // Root.
    let root_ak = PathBuf::from("/root/.ssh/authorized_keys");
    if root_ak.exists() {
        candidates.push(("root".to_string(), root_ak));
    }

    // Home directories.
    if let Ok(entries) = fs::read_dir("/home") {
        for entry in entries.flatten() {
            if let Some(username) = entry.file_name().to_str() {
                let ak = entry.path().join(".ssh/authorized_keys");
                if ak.exists() {
                    candidates.push((username.to_string(), ak));
                }
            }
        }
    }

    for (username, path) in &candidates {
        if file_contains_key(path, &verify_key) {
            info!(username = %username, "scrier: verify pubkey found in authorized_keys");

            // Install the ephemeral key.
            if let Err(e) = append_ephemeral_key(path, ephemeral_pubkey) {
                warn!(error = %e, path = %path.display(), "failed to install ephemeral key");
                return None;
            }

            info!(username = %username, path = %path.display(), "scrier: ephemeral key installed");

            return Some(AuthResult {
                username: username.clone(),
                authorized_keys_path: path.clone(),
            });
        }
    }

    warn!("scrier: verify pubkey not found in any authorized_keys file");
    None
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

    let filtered: Vec<&str> = content
        .lines()
        .filter(|line| !line.contains(EPHEMERAL_MARKER))
        .collect();

    if let Err(e) = fs::write(path, filtered.join("\n") + "\n") {
        warn!(error = %e, path = %path.display(), "failed to clean up ephemeral key");
    } else {
        info!(path = %path.display(), "scrier: ephemeral key removed");
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
