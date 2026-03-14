// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

#![cfg(windows)]

//! Windows uptime trait collector.
//!
//! Uses `wmic os get LastBootUpTime /value` and parses the WMI datetime
//! format (yyyyMMddHHmmss.ffffff+UUU) to compute uptime in seconds.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

pub fn collect() -> Result<Value> {
    let output = Command::new("wmic")
        .args(["os", "get", "LastBootUpTime", "/value"])
        .output()?;

    let text = String::from_utf8_lossy(&output.stdout).to_string();
    let boot_time_str = parse_wmic_value(&text, "LastBootUpTime").unwrap_or_default();
    let boot_epoch = parse_wmi_datetime(&boot_time_str).unwrap_or(0);

    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();

    let uptime_secs = now.saturating_sub(boot_epoch);

    Ok(json!({
        "seconds": uptime_secs,
        "boot_time": boot_time_str,
    }))
}

/// Parse a WMI datetime string like "20240115083045.123456+000" into
/// a Unix epoch timestamp (seconds).
///
/// Format: yyyyMMddHHmmss.ffffff+UUU (or -UUU for negative UTC offset).
fn parse_wmi_datetime(s: &str) -> Option<u64> {
    if s.len() < 14 {
        return None;
    }

    let year: i64 = s[0..4].parse().ok()?;
    let month: i64 = s[4..6].parse().ok()?;
    let day: i64 = s[6..8].parse().ok()?;
    let hour: i64 = s[8..10].parse().ok()?;
    let min: i64 = s[10..12].parse().ok()?;
    let sec: i64 = s[12..14].parse().ok()?;

    // Parse UTC offset in minutes from the +/-UUU suffix.
    let offset_minutes: i64 = s
        .rfind('+')
        .or_else(|| s.rfind('-').filter(|&i| i > 14))
        .and_then(|i| {
            let sign = if &s[i..i + 1] == "+" { 1i64 } else { -1i64 };
            s[i + 1..].trim().parse::<i64>().ok().map(|v| sign * v)
        })
        .unwrap_or(0);

    // Simplified conversion to Unix epoch using a basic days-from-epoch calculation.
    // This is sufficient for uptime purposes (not a general-purpose calendar library).
    let epoch = simple_epoch(year, month, day, hour, min, sec);
    let adjusted = epoch - (offset_minutes * 60);

    if adjusted > 0 {
        Some(adjusted as u64)
    } else {
        None
    }
}

/// Simplified epoch calculation (days since 1970-01-01).
fn simple_epoch(year: i64, month: i64, day: i64, hour: i64, min: i64, sec: i64) -> i64 {
    // Use the formula for Julian day number adapted for Unix epoch.
    let m = if month <= 2 { month + 12 } else { month };
    let y = if month <= 2 { year - 1 } else { year };

    let days = 365 * y + y / 4 - y / 100 + y / 400 + (153 * (m - 3) + 2) / 5 + day - 719469;
    days * 86400 + hour * 3600 + min * 60 + sec
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
