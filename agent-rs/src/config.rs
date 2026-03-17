// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Agent configuration file (`<data-dir>/config`).
//!
//! Simple `key = value` format, one per line. Lines starting with `#` are
//! comments. Written automatically during register/bootstrap, editable by hand.

use std::collections::HashMap;
use std::path::Path;

/// Load agent config from `<data_dir>/config`.
/// Returns an empty map if the file doesn't exist.
pub fn load(data_dir: &Path) -> HashMap<String, String> {
    let path = data_dir.join("config");
    let content = match std::fs::read_to_string(&path) {
        Ok(c) => c,
        Err(_) => return HashMap::new(),
    };

    let mut map = HashMap::new();
    for line in content.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if let Some((key, value)) = line.split_once('=') {
            map.insert(key.trim().to_string(), value.trim().to_string());
        }
    }
    map
}

/// Save key-value pairs to `<data_dir>/config`.
/// Merges with existing values — only overwrites keys present in `values`.
/// Empty values remove the key.
pub fn save(data_dir: &Path, values: &[(&str, &str)]) -> Result<(), std::io::Error> {
    let path = data_dir.join("config");

    let mut existing = load(data_dir);
    for (k, v) in values {
        if v.is_empty() {
            existing.remove(*k);
        } else {
            existing.insert(k.to_string(), v.to_string());
        }
    }

    let mut keys: Vec<_> = existing.keys().cloned().collect();
    keys.sort();

    let mut content = String::from("# Hobnob agent configuration\n");
    for key in &keys {
        content.push_str(&format!("{} = {}\n", key, existing[key]));
    }

    std::fs::write(&path, &content)?;

    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))?;
    }

    Ok(())
}

/// Parse `fallback_servers` from config as a comma-separated list.
/// Returns an empty vec if the key is absent or empty.
pub fn parse_fallback_servers(cfg: &HashMap<String, String>) -> Vec<String> {
    match cfg.get("fallback_servers") {
        Some(v) if !v.is_empty() => v.split(',').map(|s| s.trim().to_string()).filter(|s| !s.is_empty()).collect(),
        _ => Vec::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn load_missing_file() {
        let dir = tempfile::tempdir().unwrap();
        let cfg = load(dir.path());
        assert!(cfg.is_empty());
    }

    #[test]
    fn save_and_load() {
        let dir = tempfile::tempdir().unwrap();
        save(dir.path(), &[("server", "10.0.0.1:1530"), ("log_level", "debug")]).unwrap();

        let cfg = load(dir.path());
        assert_eq!(cfg.get("server").unwrap(), "10.0.0.1:1530");
        assert_eq!(cfg.get("log_level").unwrap(), "debug");
    }

    #[test]
    fn save_merges_existing() {
        let dir = tempfile::tempdir().unwrap();
        save(dir.path(), &[("server", "10.0.0.1:1530"), ("log_level", "debug")]).unwrap();
        save(dir.path(), &[("server", "10.0.0.2:1530")]).unwrap();

        let cfg = load(dir.path());
        assert_eq!(cfg.get("server").unwrap(), "10.0.0.2:1530");
        assert_eq!(cfg.get("log_level").unwrap(), "debug");
    }

    #[test]
    fn save_removes_empty_value() {
        let dir = tempfile::tempdir().unwrap();
        save(dir.path(), &[("server", "10.0.0.1:1530"), ("log_level", "debug")]).unwrap();
        save(dir.path(), &[("log_level", "")]).unwrap();

        let cfg = load(dir.path());
        assert_eq!(cfg.get("server").unwrap(), "10.0.0.1:1530");
        assert!(cfg.get("log_level").is_none());
    }

    #[test]
    fn comments_and_blank_lines_ignored() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config");
        std::fs::write(&path, "# comment\n\nserver = 10.0.0.1:1530\n  # another comment\n").unwrap();

        let cfg = load(dir.path());
        assert_eq!(cfg.len(), 1);
        assert_eq!(cfg.get("server").unwrap(), "10.0.0.1:1530");
    }

    #[test]
    fn parse_fallback_servers_empty() {
        let cfg = HashMap::new();
        assert!(parse_fallback_servers(&cfg).is_empty());
    }

    #[test]
    fn parse_fallback_servers_single() {
        let mut cfg = HashMap::new();
        cfg.insert("fallback_servers".into(), "peer1:1530".into());
        assert_eq!(parse_fallback_servers(&cfg), vec!["peer1:1530"]);
    }

    #[test]
    fn parse_fallback_servers_multiple() {
        let mut cfg = HashMap::new();
        cfg.insert("fallback_servers".into(), "peer1:1530, peer2:1530,peer3:1530".into());
        assert_eq!(
            parse_fallback_servers(&cfg),
            vec!["peer1:1530", "peer2:1530", "peer3:1530"]
        );
    }

    #[test]
    fn parse_fallback_servers_empty_value() {
        let mut cfg = HashMap::new();
        cfg.insert("fallback_servers".into(), "".into());
        assert!(parse_fallback_servers(&cfg).is_empty());
    }
}
