// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    // Try nvidia-smi first; fall back to /sys/class/drm scan, then to the
    // Raspberry Pi VideoCore probe.
    let mut devices = match nvidia_smi_devices() {
        Some(d) => d,
        None => {
            let mut d = drm_devices();
            if d.is_empty() {
                if let Some(vc) = videocore_device() {
                    d.push(vc);
                }
            }
            d
        }
    };

    // Sort by stable identity so the emitted array is byte-identical between
    // refreshes regardless of nvidia-smi/sysfs enumeration order (ADR-033).
    devices.sort_by_key(device_key);

    Ok(json!({
        "count": devices.len(),
        "devices": devices,
    }))
}

/// Stable ordering key spanning the three device shapes: drm devices carry
/// `device` (cardN), nvidia/VideoCore carry `name`, and `pci_device`
/// disambiguates otherwise-identical entries.
fn device_key(d: &Value) -> String {
    let pick = |k: &str| d.get(k).and_then(|v| v.as_str()).unwrap_or("");
    format!("{}|{}|{}", pick("device"), pick("name"), pick("pci_device"))
}

fn nvidia_smi_devices() -> Option<Vec<Value>> {
    // 4s internal deadline (under the 5s collector timeout): a wedged GPU can
    // leave nvidia-smi in D-state, and the outer collector timeout can't kill a
    // spawn_blocking task — route through the runner so the child is killed.
    let output = crate::traits::run_cmd_timeout(
        "nvidia-smi",
        &[
            "--query-gpu=name,driver_version,vbios_version,memory.total",
            "--format=csv,noheader,nounits",
        ],
        std::time::Duration::from_secs(4),
    )
    .ok()?;

    if !output.status.success() {
        return None;
    }

    let stdout = String::from_utf8_lossy(&output.stdout);
    let mut devices = Vec::new();

    for line in stdout.lines() {
        let parts: Vec<&str> = line.split(", ").collect();
        if parts.len() >= 4 {
            devices.push(json!({
                "vendor": "nvidia",
                "name": parts[0].trim(),
                "driver_version": parts[1].trim(),
                "vbios_version": parts[2].trim(),
                "memory_mb": parts[3].trim(),
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
            if let Ok(output) = crate::traits::run_cmd("lspci", &["-s", &pci_slot, "-m"]) {
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
    let version_out = crate::traits::run_cmd("vcgencmd", &["version"]).ok()?;
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
    if let Ok(mem_out) = crate::traits::run_cmd("vcgencmd", &["get_mem", "gpu"]) {
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
