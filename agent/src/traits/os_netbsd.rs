// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let release = super::sysctl_util::sysctl_string("kern.osrelease");

    let arch = match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        other => other,
    };

    Ok(json!({
        "family": "netbsd",
        "distro": "netbsd",
        "version": release,
        "kernel": super::sysctl_util::sysctl_string("kern.version").lines().next().unwrap_or(""),
        "arch": arch,
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
