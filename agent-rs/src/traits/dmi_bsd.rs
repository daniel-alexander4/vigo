// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! DMI/SMBIOS info for BSD and macOS.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    match std::env::consts::OS {
        "macos" => macos_hardware_info(),
        "freebsd" => freebsd_kenv_info(),
        _ => bsd_generic_info(),
    }
}

fn macos_hardware_info() -> Result<Value> {
    let output = std::process::Command::new("system_profiler")
        .args(["SPHardwareDataType", "-json"])
        .output()
        .ok();

    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    if let Ok(parsed) = serde_json::from_str::<Value>(&stdout) {
        if let Some(items) = parsed.get("SPHardwareDataType").and_then(|v| v.as_array()) {
            if let Some(hw) = items.first() {
                return Ok(json!({
                    "manufacturer": "Apple",
                    "product": hw.get("machine_model").and_then(|v| v.as_str()).unwrap_or(""),
                    "serial": hw.get("serial_number").and_then(|v| v.as_str()).unwrap_or(""),
                    "uuid": hw.get("platform_UUID").and_then(|v| v.as_str()).unwrap_or(""),
                }));
            }
        }
    }

    Ok(json!({"manufacturer": "Apple"}))
}

fn freebsd_kenv_info() -> Result<Value> {
    let manufacturer = kenv("smbios.system.maker");
    let product = kenv("smbios.system.product");
    let serial = kenv("smbios.system.serial");
    let uuid = kenv("smbios.system.uuid");

    Ok(json!({
        "manufacturer": manufacturer,
        "product": product,
        "serial": serial,
        "uuid": uuid,
    }))
}

fn kenv(key: &str) -> String {
    std::process::Command::new("kenv")
        .arg(key)
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_default()
}

fn bsd_generic_info() -> Result<Value> {
    let vendor = super::sysctl_util::sysctl_string("hw.vendor");
    let product = super::sysctl_util::sysctl_string("hw.product");
    let serial = super::sysctl_util::sysctl_string("hw.serialno");
    let uuid = super::sysctl_util::sysctl_string("hw.uuid");

    Ok(json!({
        "manufacturer": vendor,
        "product": product,
        "serial": serial,
        "uuid": uuid,
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
