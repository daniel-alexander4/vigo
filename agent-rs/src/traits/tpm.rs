// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
