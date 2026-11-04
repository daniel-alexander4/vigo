// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let (name, version, build) = sw_vers();

    let kernel = super::sysctl_util::sysctl_string("kern.osrelease");

    let arch = match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        other => other,
    };

    Ok(json!({
        "family": "darwin",
        "distro": "macos",
        "name": name,
        "version": version,
        "build": build,
        "kernel": kernel,
        "arch": arch,
    }))
}

fn sw_vers() -> (String, String, String) {
    let output = crate::traits::run_cmd("sw_vers", &[])
        .ok();

    let stdout = output
        .as_ref()
        .map(|o| String::from_utf8_lossy(&o.stdout).to_string())
        .unwrap_or_default();

    let mut name = String::new();
    let mut version = String::new();
    let mut build = String::new();

    for line in stdout.lines() {
        if let Some((key, val)) = line.split_once(':') {
            let val = val.trim();
            match key.trim() {
                "ProductName" => name = val.to_string(),
                "ProductVersion" => version = val.to_string(),
                "BuildVersion" => build = val.to_string(),
                _ => {}
            }
        }
    }

    (name, version, build)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_sw_vers_parse() {
        // sw_vers may not exist on non-macOS, but parsing shouldn't panic
        let _ = sw_vers();
    }
}
