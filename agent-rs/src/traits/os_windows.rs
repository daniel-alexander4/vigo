// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

#![cfg(windows)]

//! Windows OS trait collector.
//!
//! Reads product name and version from the Windows registry at
//! `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion` and detects
//! architecture via the `PROCESSOR_ARCHITECTURE` environment variable.

use anyhow::Result;
use serde_json::{json, Value};
use winreg::enums::*;
use winreg::RegKey;

pub fn collect() -> Result<Value> {
    let hklm = RegKey::predef(HKEY_LOCAL_MACHINE);
    let nt_key = hklm.open_subkey(r"SOFTWARE\Microsoft\Windows NT\CurrentVersion")?;

    let name: String = nt_key.get_value("ProductName").unwrap_or_default();
    let version_major: String = nt_key.get_value("CurrentVersion").unwrap_or_default();
    let build: String = nt_key.get_value("CurrentBuildNumber").unwrap_or_default();

    let version = if version_major.is_empty() {
        build.clone()
    } else {
        format!("{}.{}", version_major, build)
    };

    let proc_arch = std::env::var("PROCESSOR_ARCHITECTURE")
        .unwrap_or_default()
        .to_uppercase();

    let arch = match proc_arch.as_str() {
        "AMD64" => "amd64",
        "X86" => "386",
        "ARM64" => "arm64",
        "IA64" => "ia64",
        _ => {
            // Fall back to Rust's compile-time arch.
            match std::env::consts::ARCH {
                "x86_64" => "amd64",
                "aarch64" => "arm64",
                "x86" => "386",
                a => a,
            }
        }
    };

    Ok(json!({
        "family": "windows",
        "distro": "windows",
        "name": name,
        "version": version,
        "arch": arch,
    }))
}
