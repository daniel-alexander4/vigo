// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let mut result = serde_json::Map::new();

    let entries = match std::fs::read_dir("/sys/block") {
        Ok(e) => e,
        Err(_) => return Ok(Value::Object(result)),
    };

    for entry in entries.flatten() {
        let name = entry.file_name().to_string_lossy().to_string();

        // Skip loop, ram, and dm devices.
        if name.starts_with("loop") || name.starts_with("ram") || name.starts_with("dm-") {
            continue;
        }

        let base = entry.path();
        let mut dev = serde_json::Map::new();

        // Size in 512-byte sectors.
        if let Ok(size_str) = std::fs::read_to_string(base.join("size")) {
            let sectors: u64 = size_str.trim().parse().unwrap_or(0);
            dev.insert("size_bytes".into(), json!(sectors * 512));
        }

        // Model (optional, only for physical devices).
        if let Ok(model) = std::fs::read_to_string(base.join("device/model")) {
            let model = model.trim();
            if !model.is_empty() {
                dev.insert("model".into(), json!(model));
            }
        }

        // Read-only flag.
        if let Ok(ro_str) = std::fs::read_to_string(base.join("ro")) {
            dev.insert("readonly".into(), json!(ro_str.trim() == "1"));
        }

        result.insert(name, Value::Object(dev));
    }

    Ok(Value::Object(result))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_object() {
        if let Ok(val) = collect() {
            assert!(val.is_object());
        }
    }
}
