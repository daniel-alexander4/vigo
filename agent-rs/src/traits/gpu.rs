// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    // Try nvidia-smi first.
    if let Some(devices) = nvidia_smi_devices() {
        return Ok(json!({
            "count": devices.len(),
            "devices": devices,
        }));
    }

    // Fallback to /sys/class/drm scan.
    let devices = drm_devices();
    Ok(json!({
        "count": devices.len(),
        "devices": devices,
    }))
}

fn nvidia_smi_devices() -> Option<Vec<Value>> {
    let output = Command::new("nvidia-smi")
        .args([
            "--query-gpu=name,driver_version,vbios_version,memory.total,temperature.gpu,utilization.gpu",
            "--format=csv,noheader,nounits",
        ])
        .output()
        .ok()?;

    if !output.status.success() {
        return None;
    }

    let stdout = String::from_utf8_lossy(&output.stdout);
    let mut devices = Vec::new();

    for line in stdout.lines() {
        let parts: Vec<&str> = line.split(", ").collect();
        if parts.len() >= 6 {
            devices.push(json!({
                "vendor": "nvidia",
                "name": parts[0].trim(),
                "driver_version": parts[1].trim(),
                "vbios_version": parts[2].trim(),
                "memory_mb": parts[3].trim(),
                "temperature_c": parts[4].trim(),
                "utilization_pct": parts[5].trim(),
            }));
        }
    }

    Some(devices)
}

fn drm_devices() -> Vec<Value> {
    let mut devices = Vec::new();
    let entries = match std::fs::read_dir("/sys/class/drm") {
        Ok(e) => e,
        Err(_) => return devices,
    };

    for entry in entries.flatten() {
        let name = entry.file_name().to_string_lossy().to_string();
        if !name.starts_with("card") || name.contains('-') {
            continue;
        }

        let base = entry.path();
        let mut dev = serde_json::Map::new();
        dev.insert("device".into(), json!(name));

        // Read driver from uevent.
        if let Ok(uevent) = std::fs::read_to_string(base.join("device/uevent")) {
            for line in uevent.lines() {
                if let Some(driver) = line.strip_prefix("DRIVER=") {
                    dev.insert("driver".into(), json!(driver));
                    break;
                }
            }
        }

        devices.push(Value::Object(dev));
    }

    devices
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_expected_keys() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("count"));
        assert!(obj.contains_key("devices"));
        assert!(obj["count"].is_number());
        assert!(obj["devices"].is_array());
    }
}
