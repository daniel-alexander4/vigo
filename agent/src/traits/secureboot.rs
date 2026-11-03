// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};

/// Collect Secure Boot status.
///
/// Output:
/// ```json
/// {
///   "enabled": true,
///   "mode": "uefi"
/// }
/// ```
pub fn collect() -> Result<Value> {
    let efi_dir = std::path::Path::new("/sys/firmware/efi");
    let is_uefi = efi_dir.exists();

    if !is_uefi {
        return Ok(json!({
            "enabled": false,
            "mode": "bios"
        }));
    }

    // Check Secure Boot status via the well-known EFI variable.
    // The GUID is fixed by the UEFI spec: 8be4df61-93ca-11d2-aa0d-00e098032b8c.
    // Reading the specific file avoids scanning the entire efivars directory,
    // which can have thousands of entries and lock the kernel EFI variable store.
    let enabled = std::fs::read(
        "/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c",
    )
    .ok()
    .map(|data| {
        // EFI variable format: 4 bytes attributes + data.
        // SecureBoot value is the 5th byte (index 4): 1 = enabled.
        data.len() >= 5 && data[4] == 1
    })
    .unwrap_or(false);

    let mut result = serde_json::Map::new();
    result.insert("enabled".into(), json!(enabled));
    result.insert("mode".into(), json!("uefi"));

    // EFI firmware platform size (32 or 64 bit).
    if let Ok(bits) = std::fs::read_to_string("/sys/firmware/efi/fw_platform_size") {
        let bits = bits.trim();
        if !bits.is_empty() {
            result.insert("firmware_bits".into(), json!(bits));
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
        assert!(val.get("mode").is_some());
    }
}
