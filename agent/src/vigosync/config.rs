// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-user vigosync config + key material.
//!
//! Files live under `~<user>/.vigosync/`:
//!
//! - `config.yaml` — YAML with `encrypt: bool` and optional
//!   `extra_peers: [hostname, ...]`. Missing file = legacy behavior
//!   (no encryption, LAN-only sync).
//! - `recipients.txt` — one age public key per line, `#` comments
//!   allowed. Required when `encrypt: true`.
//! - `identity.txt` — age private key, mode 0600. Only needed on
//!   machines that should be able to decrypt (dark-receiver envoys
//!   never hold this file).

use std::fs;
use std::path::{Path, PathBuf};

use age::x25519::{Identity, Recipient};
use anyhow::{bail, Context, Result};
use serde::{Deserialize, Serialize};

/// `~<user>/.vigosync/` — the per-user config + key dir.
pub fn dot_dir(home: &Path) -> PathBuf {
    home.join(".vigosync")
}

pub fn config_path(home: &Path) -> PathBuf {
    dot_dir(home).join("config.yaml")
}

pub fn identity_path(home: &Path) -> PathBuf {
    dot_dir(home).join("identity.txt")
}

pub fn recipients_path(home: &Path) -> PathBuf {
    dot_dir(home).join("recipients.txt")
}

/// Per-user vigosync settings. Default (missing file) = today's behavior.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct UserConfig {
    /// When true, files in `~<user>/vigosync/` are encrypted in place
    /// using age with the pubkeys from `recipients.txt`. Default false.
    #[serde(default)]
    pub encrypt: bool,

    /// Hostnames of cross-subnet receivers that can't be reached via
    /// multicast. The sender POSTs state announcements to each peer's
    /// `https://<host>:1531/vigosync/announce`. Empty by default.
    #[serde(default)]
    pub extra_peers: Vec<String>,
}

/// Load the user's config file. Missing or malformed = defaults +
/// a warning. Never returns an error; absence of config is not a
/// failure condition — it's the common case.
pub fn load_config(home: &Path) -> UserConfig {
    let path = config_path(home);
    match fs::read_to_string(&path) {
        Ok(s) => serde_yaml::from_str(&s).unwrap_or_else(|e| {
            tracing::warn!(
                path = %path.display(),
                error = %e,
                "vigosync: malformed config.yaml, falling back to defaults"
            );
            UserConfig::default()
        }),
        Err(_) => UserConfig::default(),
    }
}

/// Parse `recipients.txt` into a list of age recipients. Blank lines
/// and `#`-prefixed comment lines are skipped. Returns an error if the
/// file is missing, unreadable, or yields no valid recipients — the
/// caller must fail closed (refuse to encrypt) on error.
pub fn load_recipients(home: &Path) -> Result<Vec<Recipient>> {
    let path = recipients_path(home);
    let text = fs::read_to_string(&path)
        .with_context(|| format!("reading {}", path.display()))?;
    let mut out = Vec::new();
    for (lineno, raw) in text.lines().enumerate() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let r: Recipient = line
            .parse()
            .map_err(|e: &str| anyhow::anyhow!("{}:{}: invalid age recipient: {}", path.display(), lineno + 1, e))?;
        out.push(r);
    }
    if out.is_empty() {
        bail!("{} contains no recipients", path.display());
    }
    Ok(out)
}

/// Load the user's age identity (private key) for decrypt operations.
/// Skips blank lines and `#` comments. Used by CLI verbs, not the
/// daemon — the daemon only ever encrypts.
pub fn load_identity(home: &Path) -> Result<Identity> {
    let path = identity_path(home);
    let text = fs::read_to_string(&path)
        .with_context(|| format!("reading {}", path.display()))?;
    for line in text.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if let Ok(id) = line.parse::<Identity>() {
            return Ok(id);
        }
    }
    bail!("no valid age identity found in {}", path.display())
}

#[cfg(test)]
mod tests {
    use super::*;
    use age::secrecy::ExposeSecret;
    use std::io::Write;

    #[test]
    fn load_config_missing_returns_defaults() {
        let dir = tempfile::tempdir().unwrap();
        let cfg = load_config(dir.path());
        assert!(!cfg.encrypt);
        assert!(cfg.extra_peers.is_empty());
    }

    #[test]
    fn load_config_parses_yaml() {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(dot_dir(dir.path())).unwrap();
        let yaml = "encrypt: true\nextra_peers:\n  - host-a\n  - host-b\n";
        fs::write(config_path(dir.path()), yaml).unwrap();
        let cfg = load_config(dir.path());
        assert!(cfg.encrypt);
        assert_eq!(cfg.extra_peers, vec!["host-a".to_string(), "host-b".to_string()]);
    }

    #[test]
    fn load_recipients_errors_on_missing_file() {
        let dir = tempfile::tempdir().unwrap();
        assert!(load_recipients(dir.path()).is_err());
    }

    #[test]
    fn load_recipients_skips_blank_and_comments() {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(dot_dir(dir.path())).unwrap();
        // Generate a real keypair so parsing succeeds.
        let id = Identity::generate();
        let pub_line = id.to_public().to_string();
        let mut f = fs::File::create(recipients_path(dir.path())).unwrap();
        writeln!(f, "# leading comment").unwrap();
        writeln!(f, "").unwrap();
        writeln!(f, "{}", pub_line).unwrap();
        writeln!(f, "# trailing").unwrap();
        drop(f);
        let recips = load_recipients(dir.path()).unwrap();
        assert_eq!(recips.len(), 1);
    }

    #[test]
    fn load_recipients_errors_on_empty_file() {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(dot_dir(dir.path())).unwrap();
        fs::write(recipients_path(dir.path()), "# only comments\n").unwrap();
        assert!(load_recipients(dir.path()).is_err());
    }

    #[test]
    fn load_identity_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(dot_dir(dir.path())).unwrap();
        let id = Identity::generate();
        let secret = id.to_string();
        fs::write(identity_path(dir.path()), secret.expose_secret()).unwrap();
        let loaded = load_identity(dir.path()).unwrap();
        assert_eq!(loaded.to_public().to_string(), id.to_public().to_string());
    }
}
