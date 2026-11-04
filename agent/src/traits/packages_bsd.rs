// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Package list collector for BSD platforms.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let mut packages = match std::env::consts::OS {
        "freebsd" => pkg_info_packages(),
        "openbsd" => pkg_info_packages(),
        "netbsd" => pkgin_packages(),
        "macos" => brew_packages(),
        "illumos" => ips_packages(),
        _ => Vec::new(),
    };

    // Sort by (name, version) so the list is byte-identical between refreshes
    // regardless of package-manager output order (ADR-033).
    packages.sort_by(|a, b| {
        let key = |v: &Value| {
            (
                v["name"].as_str().unwrap_or("").to_string(),
                v["version"].as_str().unwrap_or("").to_string(),
            )
        };
        key(a).cmp(&key(b))
    });

    Ok(json!({
        "count": packages.len(),
        "list": packages,
    }))
}

fn pkg_info_packages() -> Vec<Value> {
    let output = crate::traits::run_cmd("pkg", &["info", "-q"])
        .or_else(|_| crate::traits::run_cmd("pkg_info", &[]))
        .ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    stdout.lines()
        .filter(|l| !l.is_empty())
        .map(|line| {
            if let Some((name, version)) = line.rsplit_once('-') {
                json!({"name": name, "version": version})
            } else {
                json!({"name": line, "version": ""})
            }
        })
        .collect()
}

fn pkgin_packages() -> Vec<Value> {
    let output = crate::traits::run_cmd("pkgin", &["list"]).ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    stdout.lines()
        .filter(|l| !l.is_empty())
        .map(|line| {
            let parts: Vec<&str> = line.split_whitespace().collect();
            let full = parts.first().unwrap_or(&"");
            if let Some((name, version)) = full.rsplit_once('-') {
                json!({"name": name, "version": version})
            } else {
                json!({"name": full, "version": ""})
            }
        })
        .collect()
}

fn brew_packages() -> Vec<Value> {
    let output = crate::traits::run_cmd("brew", &["list", "--versions"]).ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    stdout.lines()
        .filter(|l| !l.is_empty())
        .map(|line| {
            let parts: Vec<&str> = line.split_whitespace().collect();
            let name = parts.first().unwrap_or(&"").to_string();
            let version = parts.get(1).unwrap_or(&"").to_string();
            json!({"name": name, "version": version})
        })
        .collect()
}

fn ips_packages() -> Vec<Value> {
    // pkg list -H: "NAME  VERSION  IFO"
    let output = crate::traits::run_cmd("pkg", &["list", "-H"]).ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    stdout.lines()
        .filter(|l| !l.is_empty())
        .map(|line| {
            let parts: Vec<&str> = line.split_whitespace().collect();
            let name = parts.first().unwrap_or(&"").to_string();
            let version = parts.get(1).unwrap_or(&"").to_string();
            json!({"name": name, "version": version})
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
