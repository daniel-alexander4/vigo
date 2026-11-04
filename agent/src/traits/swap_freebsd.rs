// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Swap collector for FreeBSD/OpenBSD/NetBSD.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let devices = if std::env::consts::OS == "freebsd" {
        parse_swapinfo()
    } else {
        parse_swapctl()
    };

    let count = devices.len();

    Ok(json!({
        "count": count,
        "devices": devices,
    }))
}

fn parse_swapinfo() -> Vec<Value> {
    let mut devices = Vec::new();
    let output = crate::traits::run_cmd("swapinfo", &[]).ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    for (i, line) in stdout.lines().enumerate() {
        if i == 0 { continue; }
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 4 {
            devices.push(json!({
                "path": parts[0],
                "size_kb": parts[1].parse::<i64>().unwrap_or(0),
                "used_kb": parts[2].parse::<i64>().unwrap_or(0),
            }));
        }
    }

    devices
}

fn parse_swapctl() -> Vec<Value> {
    let mut devices = Vec::new();
    let output = crate::traits::run_cmd("swapctl", &["-l"]).ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    for (i, line) in stdout.lines().enumerate() {
        if i == 0 { continue; }
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 3 {
            devices.push(json!({
                "path": parts[0],
                "size_kb": parts[1].parse::<i64>().unwrap_or(0),
                "used_kb": parts[2].parse::<i64>().unwrap_or(0),
            }));
        }
    }

    devices
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_no_panic() {
        let _ = parse_swapinfo();
        let _ = parse_swapctl();
    }
}
