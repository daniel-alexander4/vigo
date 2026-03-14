// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

#![cfg(windows)]

//! Windows processors trait collector.
//!
//! Uses `wmic cpu` to retrieve processor model, core counts, and clock speed.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let output = Command::new("wmic")
        .args([
            "cpu",
            "get",
            "Name,NumberOfCores,NumberOfLogicalProcessors,MaxClockSpeed",
            "/value",
        ])
        .output()?;

    let text = String::from_utf8_lossy(&output.stdout).to_string();
    Ok(parse_cpu(&text))
}

fn parse_cpu(output: &str) -> Value {
    let model = parse_wmic_value(output, "Name").unwrap_or_default();
    let physical_cores = parse_wmic_u64(output, "NumberOfCores");
    let logical_cores = parse_wmic_u64(output, "NumberOfLogicalProcessors");
    let frequency_mhz = parse_wmic_u64(output, "MaxClockSpeed");

    json!({
        "model": model,
        "physical_cores": physical_cores,
        "logical_cores": logical_cores,
        "frequency_mhz": frequency_mhz,
    })
}

/// Parse a single string value from wmic key=value output.
fn parse_wmic_value(output: &str, key: &str) -> Option<String> {
    let prefix = format!("{}=", key);
    for line in output.lines() {
        let line = line.trim().trim_start_matches('\u{feff}');
        if let Some(val) = line.strip_prefix(&prefix) {
            let val = val.trim();
            if !val.is_empty() {
                return Some(val.to_string());
            }
        }
    }
    None
}

/// Parse a single u64 value from wmic key=value output.
fn parse_wmic_u64(output: &str, key: &str) -> u64 {
    parse_wmic_value(output, key)
        .and_then(|v| v.parse().ok())
        .unwrap_or(0)
}
