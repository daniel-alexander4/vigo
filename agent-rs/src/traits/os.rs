// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

use anyhow::Result;
use serde_json::{json, Value};
use std::collections::HashMap;

/// Root packaging lineages used to determine os.family.
const ROOT_FAMILIES: &[&str] = &["debian", "rhel", "suse", "arch", "gentoo", "slackware"];

pub fn collect() -> Result<Value> {
    let release = parse_os_release(
        &std::fs::read_to_string("/etc/os-release").unwrap_or_default(),
    );

    let family = family_from_release(&release);
    let distro = release.get("ID").cloned().unwrap_or_default();
    let version = release.get("VERSION_ID").cloned().unwrap_or_default();

    let kernel = std::fs::read_to_string("/proc/sys/kernel/osrelease")
        .map(|s| s.trim().to_string())
        .unwrap_or_default();

    let arch = match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        "x86" => "386",
        other => other,
    };

    Ok(json!({
        "family": family,
        "distro": distro,
        "version": version,
        "kernel": kernel,
        "arch": arch,
    }))
}

/// Determine OS family from parsed os-release fields.
///
/// Family = root packaging lineage (debian, rhel, suse, arch, etc.).
/// ID_LIKE is ordered by closeness (e.g. Mint has "ubuntu debian").
/// We scan for the first known root family; if none match, use the first entry.
/// Falls back to ID if ID_LIKE is absent.
fn family_from_release(release: &HashMap<String, String>) -> String {
    release
        .get("ID_LIKE")
        .and_then(|v| {
            let parts: Vec<&str> = v.split_whitespace().collect();
            parts
                .iter()
                .find(|p| ROOT_FAMILIES.contains(&p.to_lowercase().as_str()))
                .or(parts.first())
                .map(|s| s.to_string())
        })
        .or_else(|| release.get("ID").cloned())
        .unwrap_or_default()
}

fn parse_os_release(content: &str) -> HashMap<String, String> {
    let mut map = HashMap::new();
    for line in content.lines() {
        if let Some((key, val)) = line.split_once('=') {
            map.insert(key.to_string(), val.trim_matches('"').to_string());
        }
    }
    map
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_os_release_ubuntu() {
        let content = r#"NAME="Ubuntu"
ID=ubuntu
ID_LIKE=debian
VERSION_ID="22.04"
"#;
        let m = parse_os_release(content);
        assert_eq!(m.get("ID").unwrap(), "ubuntu");
        assert_eq!(m.get("ID_LIKE").unwrap(), "debian");
        assert_eq!(m.get("VERSION_ID").unwrap(), "22.04");
    }

    #[test]
    fn test_parse_os_release_fedora() {
        let content = "ID=fedora\nVERSION_ID=39\n";
        let m = parse_os_release(content);
        assert_eq!(m.get("ID").unwrap(), "fedora");
        assert_eq!(m.get("ID_LIKE"), None);
    }

    #[test]
    fn test_collect_returns_expected_keys() {
        // May fail on non-Linux but should not panic.
        if let Ok(val) = collect() {
            let obj = val.as_object().unwrap();
            assert!(obj.contains_key("family"));
            assert!(obj.contains_key("distro"));
            assert!(obj.contains_key("version"));
            assert!(obj.contains_key("kernel"));
            assert!(obj.contains_key("arch"));
        }
    }

    #[test]
    fn test_family_mint_is_debian() {
        let release = parse_os_release("ID=linuxmint\nID_LIKE=\"ubuntu debian\"\n");
        assert_eq!(family_from_release(&release), "debian");
    }

    #[test]
    fn test_family_ubuntu_is_debian() {
        let release = parse_os_release("ID=ubuntu\nID_LIKE=debian\n");
        assert_eq!(family_from_release(&release), "debian");
    }

    #[test]
    fn test_family_rocky_is_rhel() {
        let release = parse_os_release("ID=rocky\nID_LIKE=\"rhel centos fedora\"\n");
        assert_eq!(family_from_release(&release), "rhel");
    }

    #[test]
    fn test_family_fedora_is_fedora() {
        // Fedora has no ID_LIKE, falls through to ID
        let release = parse_os_release("ID=fedora\n");
        assert_eq!(family_from_release(&release), "fedora");
    }

    #[test]
    fn test_family_unknown_uses_first_id_like() {
        let release = parse_os_release("ID=custom\nID_LIKE=\"niche obscure\"\n");
        assert_eq!(family_from_release(&release), "niche");
    }
}
