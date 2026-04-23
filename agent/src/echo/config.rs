// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-user echo config.
//!
//! Files live under `~<user>/.echo/`:
//!
//! - `config.yaml` — YAML with optional `cross_subnet_peers: [hostname,
//!   ...]`. Missing file = LAN-only multicast, no cross-subnet.
//!
//! Echo is plaintext LAN sync. If you want ciphertext at rest +
//! cross-mesh encryption, use [sigil](../sigil) instead —
//! separate subsystem, separate directory, separate threat model.
//! The 0.25.0 `encrypt: true` opt-in on echo was removed in
//! 0.26.1 in favor of sigil.

use std::fs;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

/// `~<user>/.echo/` — the per-user config dir.
pub fn dot_dir(home: &Path) -> PathBuf {
    home.join(".echo")
}

pub fn config_path(home: &Path) -> PathBuf {
    dot_dir(home).join("config.yaml")
}

/// Per-user echo settings. Default (missing file) = LAN-only,
/// no cross-subnet peers.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct UserConfig {
    /// Hostnames of cross-subnet receivers that can't be reached via
    /// multicast. The sender POSTs state announcements to each peer's
    /// `https://<host>:1531/echo/announce`. Empty by default.
    #[serde(default)]
    pub cross_subnet_peers: Vec<String>,
}

/// Load the user's config file. Missing or malformed = defaults +
/// a warning. Never returns an error; absence of config is the
/// common case.
pub fn load_config(home: &Path) -> UserConfig {
    let path = config_path(home);
    match fs::read_to_string(&path) {
        Ok(s) => serde_yaml::from_str(&s).unwrap_or_else(|e| {
            tracing::warn!(
                path = %path.display(),
                error = %e,
                "echo: malformed config.yaml, falling back to defaults"
            );
            UserConfig::default()
        }),
        Err(_) => UserConfig::default(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn load_config_missing_returns_defaults() {
        let dir = tempfile::tempdir().unwrap();
        let cfg = load_config(dir.path());
        assert!(cfg.cross_subnet_peers.is_empty());
    }

    #[test]
    fn load_config_parses_cross_subnet_peers() {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(dot_dir(dir.path())).unwrap();
        let yaml = "cross_subnet_peers:\n  - host-a\n  - host-b\n";
        fs::write(config_path(dir.path()), yaml).unwrap();
        let cfg = load_config(dir.path());
        assert_eq!(
            cfg.cross_subnet_peers,
            vec!["host-a".to_string(), "host-b".to_string()]
        );
    }

    #[test]
    fn load_config_ignores_removed_encrypt_field() {
        // The old 0.25.0 `encrypt: true` field is silently ignored
        // under serde's default behavior. Confirms we don't crash on
        // pre-0.26.1 config files left on disk.
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(dot_dir(dir.path())).unwrap();
        let yaml = "encrypt: true\ncross_subnet_peers:\n  - host-a\n";
        fs::write(config_path(dir.path()), yaml).unwrap();
        let cfg = load_config(dir.path());
        assert_eq!(cfg.cross_subnet_peers, vec!["host-a".to_string()]);
    }
}
