// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

#![cfg(windows)]

//! Windows memory trait collector.
//!
//! Uses `wmic os` to retrieve total and free physical memory, returning
//! values in the same byte-denominated JSON format as the Linux collector.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let output = Command::new("wmic")
        .args([
            "os",
            "get",
            "TotalVisibleMemorySize,FreePhysicalMemory",
            "/value",
        ])
        .output()?;

    let text = String::from_utf8_lossy(&output.stdout).to_string();
    Ok(parse_memory(&text))
}

fn parse_memory(output: &str) -> Value {
    let total_kb = parse_wmic_u64(output, "TotalVisibleMemorySize");
    let free_kb = parse_wmic_u64(output, "FreePhysicalMemory");

    let total_bytes = total_kb * 1024;
    let free_bytes = free_kb * 1024;
    let used_bytes = total_bytes.saturating_sub(free_bytes);
    // On Windows, available ~ free (no buffers/cached distinction).
    let available_bytes = free_bytes;

    let percent_used = if total_bytes > 0 {
        (used_bytes as f64 / total_bytes as f64) * 100.0
    } else {
        0.0
    };

    json!({
        "total_bytes": total_bytes,
        "free_bytes": free_bytes,
        "used_bytes": used_bytes,
        "available_bytes": available_bytes,
        "percent_used": (percent_used * 100.0).round() / 100.0,
    })
}

/// Parse a single u64 value from wmic key=value output.
fn parse_wmic_u64(output: &str, key: &str) -> u64 {
    let prefix = format!("{}=", key);
    for line in output.lines() {
        let line = line.trim().trim_start_matches('\u{feff}');
        if let Some(val) = line.strip_prefix(&prefix) {
            if let Ok(n) = val.trim().parse::<u64>() {
                return n;
            }
        }
    }
    0
}
