// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

    // Check Secure Boot status via the EFI variable.
    // The SecureBoot-8be4df61-... variable's last byte is 1 if enabled.
    let enabled = std::fs::read_dir("/sys/firmware/efi/efivars")
        .ok()
        .and_then(|entries| {
            for entry in entries.flatten() {
                let name = entry.file_name();
                let name = name.to_string_lossy();
                if name.starts_with("SecureBoot-") {
                    if let Ok(data) = std::fs::read(entry.path()) {
                        // EFI variable format: 4 bytes attributes + data.
                        // SecureBoot value is the 5th byte (index 4): 1 = enabled.
                        if data.len() >= 5 {
                            return Some(data[4] == 1);
                        }
                    }
                }
            }
            None
        })
        .unwrap_or(false);

    Ok(json!({
        "enabled": enabled,
        "mode": "uefi"
    }))
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
