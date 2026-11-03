// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Block device collector for BSD.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let devices = match std::env::consts::OS {
        "freebsd" => freebsd_disks(),
        "macos" => macos_disks(),
        _ => Vec::new(),
    };

    Ok(json!(devices))
}

fn freebsd_disks() -> Vec<Value> {
    let raw = super::sysctl_util::sysctl_string("kern.disks");
    raw.split_whitespace()
        .map(|name| json!({"name": name}))
        .collect()
}

fn macos_disks() -> Vec<Value> {
    let output = std::process::Command::new("diskutil").args(["list"]).output().ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    let mut disks = Vec::new();
    for line in stdout.lines() {
        if line.starts_with("/dev/") {
            let name = line.split_whitespace().next().unwrap_or("").to_string();
            if !name.is_empty() {
                disks.push(json!({"name": name}));
            }
        }
    }
    disks
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
