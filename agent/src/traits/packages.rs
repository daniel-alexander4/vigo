// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};
use super::{run_built_cmd, run_cmd};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let installed = collect_packages();
    Ok(json!({ "installed": installed }))
}

/// Count packages with available updates and detect broken repos.
/// This is expensive (hits package manager caches or network) and should
/// be called on a periodic schedule, not every check-in cycle.
/// Stored under trait key "package_updates".
pub fn collect_updates() -> Result<Value> {
    let count = updates_available();
    let broken = broken_repos();
    let mut result = json!({ "count": count });
    if !broken.is_empty() {
        result["broken_repos"] = json!(broken);
        result["broken_repo_count"] = json!(broken.len());
    }
    Ok(result)
}

fn updates_available() -> i64 {
    // Debian/Ubuntu: apt list --upgradable (reads local cache, no network).
    if let Some(count) = apt_upgradable() {
        return count;
    }

    // RHEL/Fedora: dnf check-update (may contact repos).
    if let Some(count) = dnf_check_update() {
        return count;
    }

    // RHEL 7/CentOS: yum check-update.
    if let Some(count) = yum_check_update() {
        return count;
    }

    // macOS: brew outdated.
    if let Some(count) = brew_outdated() {
        return count;
    }

    -1 // Unknown / unsupported package manager.
}

fn apt_upgradable() -> Option<i64> {
    let mut cmd = Command::new("apt");
    cmd.args(["list", "--upgradable"]).env("DEBIAN_FRONTEND", "noninteractive");
    let output = run_built_cmd(cmd, "apt").ok()?;
    if !output.status.success() {
        return None;
    }
    let stdout = String::from_utf8_lossy(&output.stdout);
    // First line is "Listing..." header; count the rest.
    let count = stdout.lines().skip(1).filter(|l| !l.is_empty()).count();
    Some(count as i64)
}

fn dnf_check_update() -> Option<i64> {
    let output = run_cmd("dnf", &["check-update", "--quiet"]).ok()?;
    // dnf returns exit code 100 when updates are available, 0 when none.
    match output.status.code() {
        Some(0) => Some(0),
        Some(100) => {
            let stdout = String::from_utf8_lossy(&output.stdout);
            let count = stdout.lines().filter(|l| !l.trim().is_empty()).count();
            Some(count as i64)
        }
        _ => None,
    }
}

fn yum_check_update() -> Option<i64> {
    let output = run_cmd("yum", &["check-update", "--quiet"]).ok()?;
    match output.status.code() {
        Some(0) => Some(0),
        Some(100) => {
            let stdout = String::from_utf8_lossy(&output.stdout);
            let count = stdout.lines().filter(|l| !l.trim().is_empty()).count();
            Some(count as i64)
        }
        _ => None,
    }
}

fn brew_outdated() -> Option<i64> {
    let output = run_cmd("brew", &["outdated", "--quiet"]).ok()?;
    if !output.status.success() {
        return None;
    }
    let stdout = String::from_utf8_lossy(&output.stdout);
    let count = stdout.lines().filter(|l| !l.trim().is_empty()).count();
    Some(count as i64)
}

fn collect_packages() -> Value {
    // Try dpkg (Debian/Ubuntu).
    if let Some(pkgs) = dpkg_packages() {
        return pkgs;
    }

    // Try rpm (RHEL/Fedora/SUSE).
    if let Some(pkgs) = rpm_packages() {
        return pkgs;
    }

    // Try brew (macOS).
    if let Some(pkgs) = brew_packages() {
        return pkgs;
    }

    json!({})
}

fn dpkg_packages() -> Option<Value> {
    let output = run_cmd("dpkg-query", &["-W", "-f", "${Package}\\t${Version}\\n"]).ok()?;

    if !output.status.success() {
        return None;
    }

    Some(parse_tab_separated(&String::from_utf8_lossy(&output.stdout)))
}

fn rpm_packages() -> Option<Value> {
    let output = run_cmd("rpm", &["-qa", "--queryformat", "%{NAME}\\t%{VERSION}-%{RELEASE}\\n"]).ok()?;

    if !output.status.success() {
        return None;
    }

    Some(parse_tab_separated(&String::from_utf8_lossy(&output.stdout)))
}

fn brew_packages() -> Option<Value> {
    let output = run_cmd("brew", &["list", "--versions"]).ok()?;

    if !output.status.success() {
        return None;
    }

    let mut map = serde_json::Map::new();
    for line in String::from_utf8_lossy(&output.stdout).lines() {
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 2 {
            map.insert(parts[0].to_string(), json!(parts[1]));
        }
    }
    Some(Value::Object(map))
}

fn parse_tab_separated(output: &str) -> Value {
    let mut map = serde_json::Map::new();
    for line in output.lines() {
        if let Some((name, version)) = line.split_once('\t') {
            if !name.is_empty() {
                map.insert(name.to_string(), json!(version));
            }
        }
    }
    Value::Object(map)
}

/// Detect broken package repositories by checking for fetch errors.
/// Reads the last apt-get update log or runs a dry check. Returns a
/// list of repo identifiers that failed. Zero-cost if no package
/// manager is detected.
fn broken_repos() -> Vec<String> {
    // Debian/Ubuntu: check apt update logs for Err: lines.
    if let Some(repos) = apt_broken_repos() {
        return repos;
    }
    // dnf: check for repo errors.
    if let Some(repos) = dnf_broken_repos() {
        return repos;
    }
    Vec::new()
}

fn apt_broken_repos() -> Option<Vec<String>> {
    // Check /var/log/apt/term.log for recent Err: lines, or run
    // apt-get update in simulate mode. The lightest approach: parse
    // /var/lib/apt/lists/partial/ for failed downloads.
    let partial = std::path::Path::new("/var/lib/apt/lists/partial");
    if !partial.exists() {
        return None; // Not a Debian system.
    }

    // Run apt-get update --print-uris to check repo reachability
    // without downloading. Parse stderr for Err:/Failed lines.
    let mut cmd = Command::new("apt-get");
    cmd.args(["update", "-qq", "--print-uris"]).env("DEBIAN_FRONTEND", "noninteractive");
    let output = run_built_cmd(cmd, "apt-get").ok()?;

    let stderr = String::from_utf8_lossy(&output.stderr);
    let mut broken = Vec::new();
    for line in stderr.lines() {
        // Lines like: "Err:1 https://aquasecurity.github.io/trivy-repo/deb generic/main amd64"
        // or: "E: Failed to fetch https://..."
        if line.starts_with("Err:") || line.starts_with("E: Failed to fetch") {
            // Extract the URL or repo identifier.
            let parts: Vec<&str> = line.splitn(3, ' ').collect();
            if parts.len() >= 2 {
                let repo = parts[1].trim();
                if !repo.is_empty() && !broken.contains(&repo.to_string()) {
                    broken.push(repo.to_string());
                }
            }
        }
    }
    Some(broken)
}

fn dnf_broken_repos() -> Option<Vec<String>> {
    let output = run_cmd("dnf", &["repolist", "--all", "--quiet"]).ok()?;
    if !output.status.success() {
        return None;
    }
    let stdout = String::from_utf8_lossy(&output.stdout);
    let mut broken = Vec::new();
    for line in stdout.lines() {
        // dnf repolist shows "disabled" for disabled repos — not broken.
        // Broken repos show errors in stderr or have status issues.
        // For now, just check if the command itself had errors.
        if line.contains("Error") {
            broken.push(line.trim().to_string());
        }
    }
    Some(broken)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_tab_separated() {
        let input = "bash\t5.1-6\ncurl\t7.81.0-1\n";
        let val = parse_tab_separated(input);
        let obj = val.as_object().unwrap();
        assert_eq!(obj["bash"], "5.1-6");
        assert_eq!(obj["curl"], "7.81.0-1");
    }

    #[test]
    fn test_parse_tab_separated_empty() {
        let val = parse_tab_separated("");
        assert_eq!(val.as_object().unwrap().len(), 0);
    }
}
