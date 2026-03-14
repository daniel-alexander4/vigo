// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! OS info for illumos — reads `/etc/release` and `uname`.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let (distro, version) = parse_release();

    let arch = match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        other => other,
    };

    let kernel = run_cmd("uname", &["-v"]);

    Ok(json!({
        "family": "illumos",
        "distro": distro,
        "version": version,
        "kernel": kernel,
        "arch": arch,
    }))
}

fn parse_release() -> (String, String) {
    let content = std::fs::read_to_string("/etc/release").unwrap_or_default();
    let first_line = content.lines().next().unwrap_or("").trim().to_string();

    // /etc/release first line examples:
    //   "OmniOS v11 r151048"
    //   "OpenIndiana Hipster 2023.10"
    //   "SmartOS 20231026T000825Z"
    let distro = if first_line.contains("OmniOS") {
        "omnios"
    } else if first_line.contains("OpenIndiana") {
        "openindiana"
    } else if first_line.contains("SmartOS") {
        "smartos"
    } else {
        "illumos"
    };

    // Extract version: last whitespace-delimited token.
    let version = first_line.split_whitespace().last().unwrap_or("").to_string();

    (distro.to_string(), version)
}

fn run_cmd(cmd: &str, args: &[&str]) -> String {
    std::process::Command::new(cmd)
        .args(args)
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }

    #[test]
    fn test_parse_release_omnios() {
        // parse_release reads from /etc/release which won't exist on CI,
        // but it should not panic.
        let _ = parse_release();
    }
}
