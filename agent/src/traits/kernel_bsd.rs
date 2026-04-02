// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Kernel info for macOS and BSD — uses sysctl.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let version = super::sysctl_util::sysctl_string("kern.version");
    let release = super::sysctl_util::sysctl_string("kern.osrelease");
    let hostname = super::sysctl_util::sysctl_string("kern.hostname");

    Ok(json!({
        "version": version,
        "release": release,
        "hostname": hostname,
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
