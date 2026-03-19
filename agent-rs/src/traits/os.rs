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

    // Upstream distro and codename for derivative distributions.
    // Linux Mint → ubuntu/noble, Pop!_OS → ubuntu/jammy, LMDE → debian/bookworm, etc.
    // Sources: UBUNTU_CODENAME, VERSION_CODENAME, ID_LIKE from /etc/os-release.
    let (upstream_id, upstream_codename) = upstream_from_release(&release, &distro);

    let kernel = std::fs::read_to_string("/proc/sys/kernel/osrelease")
        .map(|s| s.trim().to_string())
        .unwrap_or_default();

    // arch: normalized Go/Docker convention (amd64, arm64)
    // arch_raw: original platform convention (x86_64, aarch64) for download URLs
    let arch_raw = std::env::consts::ARCH;
    let arch = match arch_raw {
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
        "arch_raw": arch_raw,
        "upstream_id": upstream_id,
        "upstream_codename": upstream_codename,
    }))
}

/// Determine upstream distro ID and codename for derivative distributions.
///
/// For derivatives like Linux Mint, Pop!_OS, Elementary, etc., third-party
/// repos (Docker, Node, Erlang) use the upstream Ubuntu/Debian codename.
/// This extracts that from /etc/os-release fields:
///   - UBUNTU_CODENAME → upstream is ubuntu with that codename
///   - VERSION_CODENAME → fallback codename (may be the derivative's own)
///   - ID_LIKE → first entry is the immediate parent distro
///
/// For non-derivatives, returns the distro's own ID and VERSION_CODENAME.
fn upstream_from_release(release: &HashMap<String, String>, distro: &str) -> (String, String) {
    // UBUNTU_CODENAME is set by Ubuntu derivatives (Mint, Pop, Elementary, Zorin, etc.)
    if let Some(codename) = release.get("UBUNTU_CODENAME") {
        if !codename.is_empty() {
            return ("ubuntu".to_string(), codename.clone());
        }
    }

    // For non-derivatives, VERSION_CODENAME is the distro's own codename.
    // For Debian: "bookworm", "bullseye". For Ubuntu: "noble", "jammy".
    let codename = release
        .get("VERSION_CODENAME")
        .cloned()
        .unwrap_or_default();

    // If the distro has ID_LIKE and is not itself a root distro, use the
    // first parent as upstream_id. Otherwise, upstream_id == distro.
    let upstream_id = release
        .get("ID_LIKE")
        .and_then(|v| {
            let first = v.split_whitespace().next()?;
            // Don't "upstream" root distros to themselves
            if first == distro {
                None
            } else {
                Some(first.to_string())
            }
        })
        .unwrap_or_else(|| distro.to_string());

    (upstream_id, codename)
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
            for key in &["family", "distro", "version", "kernel", "arch",
                         "arch_raw", "upstream_id", "upstream_codename"] {
                assert!(obj.contains_key(*key), "missing key: {}", key);
            }
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

    // --- upstream_from_release tests ---

    #[test]
    fn test_upstream_mint_22() {
        // Linux Mint 22 has UBUNTU_CODENAME=noble
        let release = parse_os_release(
            "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=22\nVERSION_CODENAME=wilma\nUBUNTU_CODENAME=noble\n"
        );
        let (id, codename) = upstream_from_release(&release, "linuxmint");
        assert_eq!(id, "ubuntu");
        assert_eq!(codename, "noble");
    }

    #[test]
    fn test_upstream_mint_21() {
        let release = parse_os_release(
            "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=21.3\nVERSION_CODENAME=virginia\nUBUNTU_CODENAME=jammy\n"
        );
        let (id, codename) = upstream_from_release(&release, "linuxmint");
        assert_eq!(id, "ubuntu");
        assert_eq!(codename, "jammy");
    }

    #[test]
    fn test_upstream_pop_os() {
        let release = parse_os_release(
            "ID=pop\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=22.04\nUBUNTU_CODENAME=jammy\n"
        );
        let (id, codename) = upstream_from_release(&release, "pop");
        assert_eq!(id, "ubuntu");
        assert_eq!(codename, "jammy");
    }

    #[test]
    fn test_upstream_ubuntu_is_itself() {
        let release = parse_os_release(
            "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=24.04\nVERSION_CODENAME=noble\nUBUNTU_CODENAME=noble\n"
        );
        let (id, codename) = upstream_from_release(&release, "ubuntu");
        assert_eq!(id, "ubuntu");
        assert_eq!(codename, "noble");
    }

    #[test]
    fn test_upstream_debian_is_itself() {
        let release = parse_os_release(
            "ID=debian\nVERSION_ID=12\nVERSION_CODENAME=bookworm\n"
        );
        let (id, codename) = upstream_from_release(&release, "debian");
        assert_eq!(id, "debian");
        assert_eq!(codename, "bookworm");
    }

    #[test]
    fn test_upstream_rocky_linux() {
        // Rocky has no UBUNTU_CODENAME; ID_LIKE points to rhel
        let release = parse_os_release(
            "ID=rocky\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID=9.3\nVERSION_CODENAME=\"Blue Onyx\"\n"
        );
        let (id, codename) = upstream_from_release(&release, "rocky");
        assert_eq!(id, "rhel");
        assert_eq!(codename, "Blue Onyx");
    }

    #[test]
    fn test_upstream_lmde() {
        // LMDE (Linux Mint Debian Edition) — no UBUNTU_CODENAME, ID_LIKE=debian
        let release = parse_os_release(
            "ID=linuxmint\nID_LIKE=debian\nVERSION_ID=6\nVERSION_CODENAME=faye\n"
        );
        let (id, codename) = upstream_from_release(&release, "linuxmint");
        assert_eq!(id, "debian");
        assert_eq!(codename, "faye");
    }

    #[test]
    fn test_upstream_elementary() {
        let release = parse_os_release(
            "ID=elementary\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=7.1\nUBUNTU_CODENAME=jammy\n"
        );
        let (id, codename) = upstream_from_release(&release, "elementary");
        assert_eq!(id, "ubuntu");
        assert_eq!(codename, "jammy");
    }

    #[test]
    fn test_upstream_fedora_no_codename() {
        // Fedora has no ID_LIKE, no UBUNTU_CODENAME
        let release = parse_os_release("ID=fedora\nVERSION_ID=39\n");
        let (id, codename) = upstream_from_release(&release, "fedora");
        assert_eq!(id, "fedora");
        assert_eq!(codename, "");
    }
}
