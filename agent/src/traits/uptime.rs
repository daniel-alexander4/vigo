// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

use anyhow::Result;
use serde_json::{json, Value};
use std::mem::MaybeUninit;
use std::time::{SystemTime, UNIX_EPOCH};

pub fn collect() -> Result<Value> {
    let uptime_secs = parse_uptime(
        &std::fs::read_to_string("/proc/uptime").unwrap_or_default(),
    );

    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    let boot_time = now.saturating_sub(uptime_secs);
    let boot_time_str = format_rfc3339_utc(boot_time as i64);

    Ok(json!({
        "seconds": uptime_secs,
        "boot_time": boot_time_str,
    }))
}

fn parse_uptime(content: &str) -> u64 {
    content
        .split_whitespace()
        .next()
        .and_then(|s| s.parse::<f64>().ok())
        .map(|f| f as u64)
        .unwrap_or(0)
}

#[allow(deprecated)]
fn format_rfc3339_utc(timestamp: i64) -> String {
    unsafe {
        let t = timestamp as libc::time_t;
        let mut tm = MaybeUninit::<libc::tm>::uninit();
        libc::gmtime_r(&t, tm.as_mut_ptr());
        let tm = tm.assume_init();
        format!(
            "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}Z",
            tm.tm_year + 1900,
            tm.tm_mon + 1,
            tm.tm_mday,
            tm.tm_hour,
            tm.tm_min,
            tm.tm_sec,
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_uptime() {
        assert_eq!(parse_uptime("12345.67 98765.43\n"), 12345);
        assert_eq!(parse_uptime("0.01 0.02"), 0);
        assert_eq!(parse_uptime(""), 0);
    }

    #[test]
    fn test_format_rfc3339_utc() {
        // 2024-01-01T00:00:00Z = 1704067200
        let s = format_rfc3339_utc(1704067200);
        assert_eq!(s, "2024-01-01T00:00:00Z");
    }

    #[test]
    fn test_collect_returns_expected_keys() {
        if let Ok(val) = collect() {
            let obj = val.as_object().unwrap();
            assert!(obj.contains_key("seconds"));
            assert!(obj.contains_key("boot_time"));
            assert!(obj["seconds"].is_number());
            assert!(obj["boot_time"].is_string());
        }
    }
}
