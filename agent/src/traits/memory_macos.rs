// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let total = super::sysctl_util::sysctl_u64("hw.memsize").unwrap_or(0);

    // Parse vm_stat for page-level memory info.
    let (free, active, inactive, wired, _page_size) = parse_vm_stat();

    let used = active + wired;
    let available = free + inactive;

    let percent = if total > 0 {
        (used as f64 / total as f64) * 100.0
    } else {
        0.0
    };

    // Swap from sysctl vm.swapusage
    let (swap_total, swap_used, swap_free) = parse_swap_usage();
    let swap_percent = if swap_total > 0 {
        (swap_used as f64 / swap_total as f64) * 100.0
    } else {
        0.0
    };

    Ok(json!({
        "total": total,
        "available": available,
        "used": used,
        "free": free,
        "percent": (percent * 100.0).round() / 100.0,
        "swap_total": swap_total,
        "swap_used": swap_used,
        "swap_free": swap_free,
        "swap_percent": (swap_percent * 100.0).round() / 100.0,
    }))
}

fn parse_vm_stat() -> (u64, u64, u64, u64, u64) {
    let output = crate::traits::run_cmd("vm_stat", &[]).ok();
    let stdout = output
        .as_ref()
        .map(|o| String::from_utf8_lossy(&o.stdout).to_string())
        .unwrap_or_default();

    let mut page_size: u64 = 4096;
    let mut free: u64 = 0;
    let mut active: u64 = 0;
    let mut inactive: u64 = 0;
    let mut wired: u64 = 0;

    for line in stdout.lines() {
        if line.contains("page size of") {
            if let Some(n) = line.split_whitespace().rev().next() {
                if let Ok(ps) = n.trim_end_matches(')').parse::<u64>() {
                    page_size = ps;
                }
            }
        }
        if let Some((key, val)) = line.split_once(':') {
            let pages: u64 = val.trim().trim_end_matches('.').parse().unwrap_or(0);
            let bytes = pages * page_size;
            match key.trim() {
                "Pages free" => free = bytes,
                "Pages active" => active = bytes,
                "Pages inactive" => inactive = bytes,
                "Pages wired down" => wired = bytes,
                _ => {}
            }
        }
    }

    (free, active, inactive, wired, page_size)
}

fn parse_swap_usage() -> (u64, u64, u64) {
    let raw = super::sysctl_util::sysctl_string("vm.swapusage");
    if raw.is_empty() {
        return (0, 0, 0);
    }

    // "total = 2048.00M  used = 100.00M  free = 1948.00M"
    let mut total: u64 = 0;
    let mut used: u64 = 0;
    let mut free: u64 = 0;

    for part in raw.split("  ") {
        if let Some((key, val)) = part.trim().split_once(" = ") {
            let bytes = parse_size_mb(val.trim());
            match key.trim() {
                "total" => total = bytes,
                "used" => used = bytes,
                "free" => free = bytes,
                _ => {}
            }
        }
    }

    (total, used, free)
}

fn parse_size_mb(s: &str) -> u64 {
    let s = s.trim();
    if let Some(n) = s.strip_suffix('M') {
        n.parse::<f64>().map(|f| (f * 1024.0 * 1024.0) as u64).unwrap_or(0)
    } else if let Some(n) = s.strip_suffix('G') {
        n.parse::<f64>().map(|f| (f * 1024.0 * 1024.0 * 1024.0) as u64).unwrap_or(0)
    } else if let Some(n) = s.strip_suffix('K') {
        n.parse::<f64>().map(|f| (f * 1024.0) as u64).unwrap_or(0)
    } else {
        s.parse::<u64>().unwrap_or(0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_size_mb() {
        assert_eq!(parse_size_mb("2048.00M"), 2048 * 1024 * 1024);
        assert_eq!(parse_size_mb("1.50G"), (1.5 * 1024.0 * 1024.0 * 1024.0) as u64);
        assert_eq!(parse_size_mb("512K"), 512 * 1024);
    }

    #[test]
    fn test_parse_swap_usage_empty() {
        let (t, u, f) = parse_swap_usage();
        // May or may not return values depending on OS
        let _ = (t, u, f);
    }
}
