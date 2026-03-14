// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Uptime collector for illumos — uses `kstat` for boot time.

use anyhow::Result;
use serde_json::{json, Value};
use std::time::{SystemTime, UNIX_EPOCH};

pub fn collect() -> Result<Value> {
    let boot_secs = kstat_boot_time();
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

fn kstat_boot_time() -> u64 {
    // kstat -p unix:0:system_misc:boot_time
    std::process::Command::new("kstat")
        .args(["-p", "unix:0:system_misc:boot_time"])
        .output()
        .ok()
        .filter(|o| o.status.success())
        .and_then(|o| {
            let stdout = String::from_utf8_lossy(&o.stdout);
            stdout.split('\t').nth(1)
                .and_then(|v| v.trim().parse().ok())
        })
        .unwrap_or(0)
}

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
    fn test_collect_no_panic() {
        let _ = collect();
    }

    #[test]
    fn test_format_rfc3339_utc() {
        let s = format_rfc3339_utc(1704067200);
        assert_eq!(s, "2024-01-01T00:00:00Z");
    }
}
