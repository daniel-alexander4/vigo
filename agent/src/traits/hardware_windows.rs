// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

#![cfg(windows)]

//! Windows hardware trait collector.
//!
//! Uses `wmic` commands to gather CPU count, total memory, and disk sizes.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let cpu_count = wmic_cpu_count();
    let memory_mb = wmic_memory_mb();
    let disk_gb = wmic_disk_gb();

    Ok(json!({
        "cpu_count": cpu_count,
        "memory_mb": memory_mb,
        "disk_gb": disk_gb,
    }))
}

fn wmic_cpu_count() -> u64 {
    let output = Command::new("wmic")
        .args(["cpu", "get", "NumberOfLogicalProcessors", "/value"])
        .output()
        .ok();

    output
        .and_then(|o| {
            let text = String::from_utf8_lossy(&o.stdout).to_string();
            parse_wmic_value(&text, "NumberOfLogicalProcessors")?
                .parse::<u64>()
                .ok()
        })
        .unwrap_or(1)
}

fn wmic_memory_mb() -> u64 {
    let output = Command::new("wmic")
        .args(["os", "get", "TotalVisibleMemorySize", "/value"])
        .output()
        .ok();

    output
        .and_then(|o| {
            let text = String::from_utf8_lossy(&o.stdout).to_string();
            parse_wmic_value(&text, "TotalVisibleMemorySize")?
                .parse::<u64>()
                .ok()
                .map(|kb| kb / 1024)
        })
        .unwrap_or(0)
}

fn wmic_disk_gb() -> u64 {
    let output = Command::new("wmic")
        .args(["logicaldisk", "where", "DriveType=3", "get", "Size", "/value"])
        .output()
        .ok();

    output
        .map(|o| {
            let text = String::from_utf8_lossy(&o.stdout).to_string();
            parse_wmic_values(&text, "Size")
                .iter()
                .filter_map(|v| v.parse::<u64>().ok())
                .sum::<u64>()
                / (1024 * 1024 * 1024)
        })
        .unwrap_or(0)
}

/// Parse a single key=value pair from wmic output.
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

/// Parse multiple values for the same key from wmic output (e.g., multiple disks).
fn parse_wmic_values(output: &str, key: &str) -> Vec<String> {
    let prefix = format!("{}=", key);
    let mut values = Vec::new();
    for line in output.lines() {
        let line = line.trim().trim_start_matches('\u{feff}');
        if let Some(val) = line.strip_prefix(&prefix) {
            let val = val.trim();
            if !val.is_empty() {
                values.push(val.to_string());
            }
        }
    }
    values
}
