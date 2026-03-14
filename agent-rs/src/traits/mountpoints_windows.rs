// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

#![cfg(windows)]

//! Windows mountpoints trait collector.
//!
//! Uses `wmic logicaldisk` to enumerate fixed drives with filesystem type,
//! total/used/free sizes in GB, and usage percentage.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let output = Command::new("wmic")
        .args([
            "logicaldisk",
            "get",
            "DeviceID,FileSystem,Size,FreeSpace,Description",
            "/value",
        ])
        .output()?;

    let text = String::from_utf8_lossy(&output.stdout).to_string();
    Ok(parse_mountpoints(&text))
}

fn parse_mountpoints(output: &str) -> Value {
    let mut mounts = serde_json::Map::new();
    let blocks = split_wmic_blocks(output);

    for block in &blocks {
        let device_id = parse_block_value(block, "DeviceID");
        let filesystem = parse_block_value(block, "FileSystem").unwrap_or_default();
        let size = parse_block_u64(block, "Size");
        let free = parse_block_u64(block, "FreeSpace");

        if let Some(id) = device_id {
            // Skip drives with zero size (empty/unmounted).
            if size == 0 {
                continue;
            }

            let used = size.saturating_sub(free);
            let gb = 1024 * 1024 * 1024;
            let total_gb = size / gb;
            let used_gb = used / gb;
            let free_gb = free / gb;

            let percent = if size > 0 {
                (used as f64 / size as f64) * 100.0
            } else {
                0.0
            };

            mounts.insert(
                id,
                json!({
                    "filesystem": filesystem,
                    "total_gb": total_gb,
                    "used_gb": used_gb,
                    "free_gb": free_gb,
                    "percent_used": (percent * 100.0).round() / 100.0,
                }),
            );
        }
    }

    json!({ "mounts": mounts })
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
