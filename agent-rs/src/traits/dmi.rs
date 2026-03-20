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

    Ok(Value::Object(result))
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
