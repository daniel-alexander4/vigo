// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Uptime collector for macOS and BSD — uses `sysctl kern.boottime`.

use anyhow::Result;
use serde_json::{json, Value};
use std::time::{SystemTime, UNIX_EPOCH};

pub fn collect() -> Result<Value> {
    let boot_secs = parse_boottime(&super::sysctl_util::sysctl_string("kern.boottime"));
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    let uptime = now.saturating_sub(boot_secs);
    let boot_time_str = format_rfc3339_utc(boot_secs as i64);

    Ok(json!({
        "seconds": uptime,
        "boot_time": boot_time_str,
    }))
}

fn parse_boottime(s: &str) -> u64 {
    // Format: "{ sec = 1234567890, usec = 123456 } Thu Jan ..."
    s.split("sec = ")
        .nth(1)
        .and_then(|rest| rest.split(',').next())
        .and_then(|n| n.trim().parse().ok())
        .unwrap_or(0)
}

#[allow(deprecated)]
fn format_rfc3339_utc(timestamp: i64) -> String {
    unsafe {
        let t = timestamp as libc::time_t;
        let mut tm = std::mem::MaybeUninit::<libc::tm>::uninit();
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
    fn test_parse_boottime() {
        assert_eq!(
            parse_boottime("{ sec = 1704067200, usec = 123456 } Mon Jan  1"),
            1704067200
        );
        assert_eq!(parse_boottime(""), 0);
    }

    #[test]
    fn test_format_rfc3339_utc() {
        let s = format_rfc3339_utc(1704067200);
        assert_eq!(s, "2024-01-01T00:00:00Z");
    }
}
