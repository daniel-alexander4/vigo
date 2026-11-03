// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Timezone collector for macOS and BSD.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let tz = detect_timezone();

    Ok(json!({
        "timezone": tz,
    }))
}

fn detect_timezone() -> String {
    // Try readlink on /etc/localtime
    if let Ok(target) = std::fs::read_link("/etc/localtime") {
        let s = target.to_string_lossy();
        if let Some(pos) = s.find("/zoneinfo/") {
            return s[pos + "/zoneinfo/".len()..].to_string();
        }
    }
    // macOS: try systemsetup
    if let Ok(output) = std::process::Command::new("systemsetup").args(["-gettimezone"]).output() {
        if output.status.success() {
            let stdout = String::from_utf8_lossy(&output.stdout);
            if let Some(tz) = stdout.trim().strip_prefix("Time Zone: ") {
                return tz.to_string();
            }
        }
    }
    String::new()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_detect_timezone_no_panic() {
        let _ = detect_timezone();
    }
}
