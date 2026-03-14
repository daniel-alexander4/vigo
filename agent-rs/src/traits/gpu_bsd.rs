// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! GPU info for macOS and BSD.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    match std::env::consts::OS {
        "macos" => macos_gpu(),
        _ => Ok(json!([])),
    }
}

fn macos_gpu() -> Result<Value> {
    let output = std::process::Command::new("system_profiler")
        .args(["SPDisplaysDataType", "-json"])
        .output()
        .ok();

    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    if let Ok(parsed) = serde_json::from_str::<Value>(&stdout) {
        if let Some(items) = parsed.get("SPDisplaysDataType").and_then(|v| v.as_array()) {
            let gpus: Vec<Value> = items.iter().map(|item| {
                json!({
                    "name": item.get("sppci_model").and_then(|v| v.as_str()).unwrap_or(""),
                    "vendor": item.get("sppci_vendor").and_then(|v| v.as_str()).unwrap_or(""),
                    "vram": item.get("sppci_vram").and_then(|v| v.as_str()).unwrap_or(""),
                })
            }).collect();
            return Ok(json!(gpus));
        }
    }

    Ok(json!([]))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
