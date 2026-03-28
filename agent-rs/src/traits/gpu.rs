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
    let mut devices = drm_devices();

    // ARM SBC fallback: Raspberry Pi VideoCore via vcgencmd.
    if devices.is_empty() {
        if let Some(vc) = videocore_device() {
            devices.push(vc);
        }
    }

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

        // Parse uevent for driver and PCI slot.
        let mut pci_slot = String::new();
        if let Ok(uevent) = std::fs::read_to_string(base.join("device/uevent")) {
            for line in uevent.lines() {
                if let Some(driver) = line.strip_prefix("DRIVER=") {
                    dev.insert("driver".into(), json!(driver));
                }
                if let Some(slot) = line.strip_prefix("PCI_SLOT_NAME=") {
                    pci_slot = slot.to_string();
                }
            }
        }

        // PCI vendor/device IDs (hex, zero-cost sysfs reads).
        if let Ok(vid) = std::fs::read_to_string(base.join("device/vendor")) {
            dev.insert("pci_vendor".into(), json!(vid.trim()));
        }
        if let Ok(did) = std::fs::read_to_string(base.join("device/device")) {
            dev.insert("pci_device".into(), json!(did.trim()));
        }

        // Firmware version (driver-dependent, may not exist).
        if let Ok(fw) = std::fs::read_to_string(base.join("device/fw_version")) {
            let fw = fw.trim();
            if !fw.is_empty() {
                dev.insert("firmware_version".into(), json!(fw));
            }
        }

        // Resolve human-readable name via lspci (single device, instant).
        if !pci_slot.is_empty() {
            if let Ok(output) = Command::new("lspci").args(["-s", &pci_slot, "-m"]).output() {
                if output.status.success() {
                    let line = String::from_utf8_lossy(&output.stdout);
                    // Machine-readable format: slot "class" "vendor" "device" ...
                    let fields = parse_lspci_machine(&line);
                    if let Some(vendor) = fields.get(2) {
                        dev.insert("vendor".into(), json!(vendor));
                    }
                    if let Some(model) = fields.get(3) {
                        dev.insert("name".into(), json!(model));
                    }
                }
            }
        }

        devices.push(Value::Object(dev));
    }

    devices
}

/// Parse lspci -m output: fields are quoted strings separated by spaces.
fn parse_lspci_machine(line: &str) -> Vec<String> {
    let mut fields = Vec::new();
    let mut chars = line.chars().peekable();
    while let Some(&c) = chars.peek() {
        if c == '"' {
            chars.next(); // skip opening quote
            let mut field = String::new();
            for ch in chars.by_ref() {
                if ch == '"' {
                    break;
                }
                field.push(ch);
            }
            fields.push(field);
        } else if c.is_whitespace() {
            chars.next();
        } else {
            // Unquoted field (the PCI slot at the start).
            let mut field = String::new();
            while let Some(&ch) = chars.peek() {
                if ch.is_whitespace() {
                    break;
                }
                field.push(ch);
                chars.next();
            }
            fields.push(field);
        }
    }
    fields
}

/// Detect Raspberry Pi VideoCore GPU via vcgencmd.
fn videocore_device() -> Option<Value> {
    // Check if vcgencmd exists.
    let version_out = Command::new("vcgencmd").arg("version").output().ok()?;
    if !version_out.status.success() {
        return None;
    }

    let mut dev = serde_json::Map::new();
    dev.insert("vendor".into(), json!("Broadcom"));
    dev.insert("name".into(), json!("VideoCore"));

    // Firmware version from vcgencmd version output.
    let version_str = String::from_utf8_lossy(&version_out.stdout);
    for line in version_str.lines() {
        let line = line.trim();
        if line.starts_with("version ") {
            dev.insert("firmware_version".into(), json!(line.trim_start_matches("version ").trim()));
        }
        // Some versions include a date line like "Jan  1 2026 12:00:00"
        if line.contains("Copyright") {
            continue;
        }
    }

    // GPU memory allocation.
    if let Ok(mem_out) = Command::new("vcgencmd").args(["get_mem", "gpu"]).output() {
        if mem_out.status.success() {
            let s = String::from_utf8_lossy(&mem_out.stdout);
            // Output: "gpu=128M" or "gpu=256M"
            if let Some(val) = s.trim().strip_prefix("gpu=") {
                let val = val.trim_end_matches('M').trim();
                if let Ok(mb) = val.parse::<u64>() {
                    dev.insert("memory_mb".into(), json!(mb));
                }
            }
        }
    }

    // Clock speed.
    if let Ok(clk_out) = Command::new("vcgencmd").args(["measure_clock", "core"]).output() {
        if clk_out.status.success() {
            let s = String::from_utf8_lossy(&clk_out.stdout);
            // Output: "frequency(48)=500000000" (Hz)
            if let Some(eq_pos) = s.find('=') {
                if let Ok(hz) = s[eq_pos + 1..].trim().parse::<u64>() {
                    dev.insert("clock_mhz".into(), json!(hz / 1_000_000));
                }
            }
        }
    }

    // Temperature.
    if let Ok(temp_out) = Command::new("vcgencmd").args(["measure_temp"]).output() {
        if temp_out.status.success() {
            let s = String::from_utf8_lossy(&temp_out.stdout);
            // Output: "temp=42.0'C"
            if let Some(val) = s.trim().strip_prefix("temp=") {
                let val = val.trim_end_matches("'C").trim();
                dev.insert("temperature_c".into(), json!(val));
            }
        }
    }

    Some(Value::Object(dev))
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
