// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let release = super::sysctl_util::sysctl_string("kern.osrelease");
    let version = super::sysctl_util::sysctl_string("kern.version");

    let arch = match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        other => other,
    };

    Ok(json!({
        "family": "openbsd",
        "distro": "openbsd",
        "version": release,
        "kernel": version.lines().next().unwrap_or(""),
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
