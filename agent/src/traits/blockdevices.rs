// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

use anyhow::Result;
use serde_json::{json, Value};
use std::path::Path;

pub fn collect() -> Result<Value> {
    Ok(collect_from(Path::new("/sys/block")))
}

fn collect_from(block_dir: &Path) -> Value {
    let mut result = serde_json::Map::new();

    let entries = match std::fs::read_dir(block_dir) {
        Ok(e) => e,
        Err(_) => return Value::Object(result),
    };

    for entry in entries.flatten() {
        let name = entry.file_name().to_string_lossy().to_string();

        // Skip loop, ram, and dm devices.
        if name.starts_with("loop") || name.starts_with("ram") || name.starts_with("dm-") {
            continue;
        }

        result.insert(name, Value::Object(parse_device(&entry.path())));
    }

    Value::Object(result)
}

/// Builds the per-device trait map from its `/sys/block/<dev>` base path.
fn parse_device(base: &Path) -> serde_json::Map<String, Value> {
    let mut dev = serde_json::Map::new();

    // Size in 512-byte sectors.
    if let Ok(size_str) = std::fs::read_to_string(base.join("size")) {
        let sectors: u64 = size_str.trim().parse().unwrap_or(0);
        dev.insert("size_bytes".into(), json!(sectors * 512));
    }

    // Identity + firmware (optional, only for physical devices). vendor is
    // present on SCSI/SATA; serial is present on NVMe and many SCSI/SATA
    // disks but absent on some — emit only what sysfs exposes.
    for (file, key) in [
        ("device/vendor", "vendor"),
        ("device/model", "model"),
        ("device/serial", "serial"),
        ("device/firmware_rev", "firmware_rev"),
    ] {
        if let Ok(val) = std::fs::read_to_string(base.join(file)) {
            let val = val.trim();
            if !val.is_empty() {
                dev.insert(key.into(), json!(val));
            }
        }
    }

    // Read-only flag.
    if let Ok(ro_str) = std::fs::read_to_string(base.join("ro")) {
        dev.insert("readonly".into(), json!(ro_str.trim() == "1"));
    }

    dev
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    #[test]
    fn test_collect_returns_object() {
        assert!(collect().unwrap().is_object());
    }

    #[test]
    fn test_collect_from_missing_dir_is_empty() {
        assert_eq!(collect_from(Path::new("/nonexistent/sys/block")), json!({}));
    }

    #[test]
    fn test_parse_device_identity_and_skips() {
        let tmp = std::env::temp_dir().join(format!("vigo-blockdev-test-{}", std::process::id()));
        let _ = fs::remove_dir_all(&tmp);
        // A physical NVMe-style device with full identity.
        let nvme = tmp.join("nvme0n1");
        fs::create_dir_all(nvme.join("device")).unwrap();
        fs::write(nvme.join("size"), "1000215216\n").unwrap();
        fs::write(nvme.join("ro"), "0\n").unwrap();
        fs::write(nvme.join("device/vendor"), "Samsung\n").unwrap();
        fs::write(nvme.join("device/model"), "SSD 980 PRO\n").unwrap();
        fs::write(nvme.join("device/serial"), "S5GXNX0R123456\n").unwrap();
        // A loop device that must be skipped.
        fs::create_dir_all(tmp.join("loop0")).unwrap();

        let out = collect_from(&tmp);
        let map = out.as_object().unwrap();
        assert!(!map.contains_key("loop0"), "loop devices must be skipped");
        let dev = map["nvme0n1"].as_object().unwrap();
        assert_eq!(dev["vendor"], json!("Samsung"));
        assert_eq!(dev["model"], json!("SSD 980 PRO"));
        assert_eq!(dev["serial"], json!("S5GXNX0R123456"));
        assert_eq!(dev["size_bytes"], json!(1000215216u64 * 512));
        assert_eq!(dev["readonly"], json!(false));
        // Absent firmware_rev is omitted, not null.
        assert!(!dev.contains_key("firmware_rev"));

        let _ = fs::remove_dir_all(&tmp);
    }
}
