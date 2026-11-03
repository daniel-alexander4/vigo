// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};

/// Collect TPM (Trusted Platform Module) information.
///
/// Output:
/// ```json
/// {
///   "present": true,
///   "version": "2.0"
/// }
/// ```
pub fn collect() -> Result<Value> {
    let present = std::path::Path::new("/sys/class/tpm/tpm0").exists();
    if !present {
        return Ok(json!({ "present": false }));
    }

    let version = std::fs::read_to_string("/sys/class/tpm/tpm0/tpm_version_major")
        .ok()
        .map(|s| s.trim().to_string())
        .and_then(|v| if v.is_empty() { None } else { Some(v) });

    let mut result = serde_json::Map::new();
    result.insert("present".into(), json!(true));
    if let Some(v) = version {
        let display = match v.as_str() {
            "2" => "2.0".to_string(),
            "1" => "1.2".to_string(),
            other => other.to_string(),
        };
        result.insert("version".into(), json!(display));
    }

    // Firmware version (device/driver dependent, may not be exposed).
    if let Ok(fw) = std::fs::read_to_string("/sys/class/tpm/tpm0/firmware_version") {
        let fw = fw.trim();
        if !fw.is_empty() {
            result.insert("firmware_version".into(), json!(fw));
        }
    }

    // Manufacturer info.
    if let Ok(caps) = std::fs::read_to_string("/sys/class/tpm/tpm0/caps") {
        for line in caps.lines() {
            if let Some(val) = line.strip_prefix("Manufacturer: ") {
                let val = val.trim();
                if !val.is_empty() {
                    result.insert("manufacturer".into(), json!(val));
                }
            }
        }
    }

    Ok(Value::Object(result))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_object() {
        let val = collect().unwrap();
        assert!(val.is_object());
        assert!(val.get("present").is_some());
    }
}
