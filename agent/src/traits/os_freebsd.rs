// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let version = freebsd_version();
    let release = super::sysctl_util::sysctl_string("kern.osrelease");

    let arch = match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        other => other,
    };

    Ok(json!({
        "family": "freebsd",
        "distro": "freebsd",
        "version": version,
        "kernel": release,
        "arch": arch,
    }))
}

fn freebsd_version() -> String {
    crate::traits::run_cmd("freebsd-version", &[])
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_else(|| super::sysctl_util::sysctl_string("kern.osrelease"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
