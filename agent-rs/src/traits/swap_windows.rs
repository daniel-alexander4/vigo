// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

#![cfg(windows)]

//! Windows swap (pagefile) trait collector.
//!
//! Uses `wmic pagefile get AllocatedBaseSize,CurrentUsage /value` to report
//! pagefile total and used sizes in bytes.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let output = Command::new("wmic")
        .args([
            "pagefile",
            "get",
            "AllocatedBaseSize,CurrentUsage",
            "/value",
        ])
        .output()?;

    let text = String::from_utf8_lossy(&output.stdout).to_string();
    Ok(parse_pagefile(&text))
}

fn parse_pagefile(output: &str) -> Value {
    // AllocatedBaseSize is in MB, CurrentUsage is in MB.
    let allocated_mb = parse_wmic_u64(output, "AllocatedBaseSize");
    let usage_mb = parse_wmic_u64(output, "CurrentUsage");

    let total_bytes = allocated_mb * 1024 * 1024;
    let used_bytes = usage_mb * 1024 * 1024;

    json!({
        "total_bytes": total_bytes,
        "used_bytes": used_bytes,
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
