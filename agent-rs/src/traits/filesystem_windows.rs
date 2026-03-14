// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

#![cfg(windows)]

//! Windows filesystem trait collector.
//!
//! Uses `wmic logicaldisk` to enumerate fixed drives and report per-drive
//! total, used, free bytes and usage percentage.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let output = Command::new("wmic")
        .args([
            "logicaldisk",
            "where",
            "DriveType=3",
            "get",
            "DeviceID,Size,FreeSpace",
            "/value",
        ])
        .output()?;

    let text = String::from_utf8_lossy(&output.stdout).to_string();
    Ok(parse_disks(&text))
}

fn parse_disks(output: &str) -> Value {
    let mut result = serde_json::Map::new();
    let blocks = split_wmic_blocks(output);

    for block in &blocks {
        let device_id = parse_block_value(block, "DeviceID");
        let size = parse_block_u64(block, "Size");
        let free = parse_block_u64(block, "FreeSpace");

        if let Some(id) = device_id {
            let used = size.saturating_sub(free);
            let percent = if size > 0 {
                (used as f64 / size as f64) * 100.0
            } else {
                0.0
            };

            result.insert(
                id,
                json!({
                    "total_bytes": size,
                    "used_bytes": used,
                    "free_bytes": free,
                    "percent_used": (percent * 100.0).round() / 100.0,
                }),
            );
        }
    }

    Value::Object(result)
}

/// Split wmic output into per-record blocks separated by blank lines.
fn split_wmic_blocks(output: &str) -> Vec<String> {
    let mut blocks = Vec::new();
    let mut current = String::new();

    for line in output.lines() {
        let line = line.trim().trim_start_matches('\u{feff}');
        if line.is_empty() {
            if !current.is_empty() {
                blocks.push(current.clone());
                current.clear();
            }
        } else {
            if !current.is_empty() {
                current.push('\n');
            }
            current.push_str(line);
        }
    }
    if !current.is_empty() {
        blocks.push(current);
    }
    blocks
}

/// Extract a string value for a key from a wmic block.
fn parse_block_value(block: &str, key: &str) -> Option<String> {
    let prefix = format!("{}=", key);
    for line in block.lines() {
        if let Some(val) = line.strip_prefix(&prefix) {
            let val = val.trim();
            if !val.is_empty() {
                return Some(val.to_string());
            }
        }
    }
    None
}

/// Extract a u64 value for a key from a wmic block.
fn parse_block_u64(block: &str, key: &str) -> u64 {
    parse_block_value(block, key)
        .and_then(|v| v.parse().ok())
        .unwrap_or(0)
}
