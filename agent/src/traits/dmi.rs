// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

use anyhow::Result;
use serde_json::{json, Value};

const DMI_BASE: &str = "/sys/class/dmi/id";

const DMI_FIELDS: &[(&str, &str)] = &[
    ("bios_vendor", "bios_vendor"),
    ("bios_version", "bios_version"),
    ("bios_date", "bios_date"),
    ("board_vendor", "board_vendor"),
    ("board_name", "board_name"),
    ("product_name", "product_name"),
    ("product_serial", "product_serial"),
    ("product_vendor", "sys_vendor"),
    ("chassis_type", "chassis_type"),
];

pub fn collect() -> Result<Value> {
    let mut result = serde_json::Map::new();

    for &(key, file) in DMI_FIELDS {
        let path = format!("{}/{}", DMI_BASE, file);
        if let Ok(val) = std::fs::read_to_string(&path) {
            let val = val.trim();
            if !val.is_empty() {
                result.insert(key.to_string(), json!(val));
            }
        }
    }

    // ARM/AArch64 boards (Raspberry Pi, etc.) don't have DMI/SMBIOS.
    // Fall back to Device Tree and /proc/cpuinfo.
    if result.is_empty() {
        collect_device_tree(&mut result);
    }

    Ok(Value::Object(result))
}

/// Collect hardware info from Device Tree and /proc/cpuinfo (ARM boards).
fn collect_device_tree(result: &mut serde_json::Map<String, Value>) {
    // /proc/device-tree/model — e.g., "Raspberry Pi 4 Model B Rev 1.4"
    if let Ok(val) = std::fs::read_to_string("/proc/device-tree/model") {
        let val = val.trim_end_matches('\0').trim();
        if !val.is_empty() {
            result.insert("product_name".to_string(), json!(val));
        }
    }

    // /proc/device-tree/compatible — e.g., "raspberrypi,4-model-b\0brcm,bcm2711"
    if let Ok(val) = std::fs::read_to_string("/proc/device-tree/compatible") {
        let parts: Vec<&str> = val.split('\0').filter(|s| !s.is_empty()).collect();
        if let Some(first) = parts.first() {
            if let Some((vendor, _)) = first.split_once(',') {
                result.insert("board_vendor".to_string(), json!(vendor));
            }
        }
    }

    // /proc/device-tree/serial-number
    if let Ok(val) = std::fs::read_to_string("/proc/device-tree/serial-number") {
        let val = val.trim_end_matches('\0').trim();
        if !val.is_empty() {
            result.insert("serial_number".to_string(), json!(val));
        }
    }

    // /proc/cpuinfo on ARM has Hardware, Revision, Serial, Model fields.
    if let Ok(cpuinfo) = std::fs::read_to_string("/proc/cpuinfo") {
        for line in cpuinfo.lines() {
            if let Some((k, v)) = line.split_once(':') {
                let k = k.trim();
                let v = v.trim();
                match k {
                    "Hardware" => {
                        if !result.contains_key("board_name") {
                            result.insert("board_name".to_string(), json!(v));
                        }
                    }
                    "Revision" => {
                        result.insert("board_revision".to_string(), json!(v));
                    }
                    "Model" => {
                        if !result.contains_key("product_name") {
                            result.insert("product_name".to_string(), json!(v));
                        }
                    }
                    _ => {}
                }
            }
        }
    }
}

/// Read a DMI field by filename. Used by the cloud collector.
pub(crate) fn read_dmi_field(filename: &str) -> Option<String> {
    std::fs::read_to_string(format!("{}/{}", DMI_BASE, filename))
        .ok()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_object() {
        let val = collect().unwrap();
        assert!(val.is_object());
    }
}
