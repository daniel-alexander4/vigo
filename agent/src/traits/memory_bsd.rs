// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Memory collector for BSD.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let total = super::sysctl_util::sysctl_u64("hw.physmem").unwrap_or(0);

    let page_size = super::sysctl_util::sysctl_u64("hw.pagesize").unwrap_or(4096);
    let free_pages = super::sysctl_util::sysctl_u64("vm.stats.vm.v_free_count").unwrap_or(0);
    let inactive_pages = super::sysctl_util::sysctl_u64("vm.stats.vm.v_inactive_count").unwrap_or(0);

    let free = free_pages * page_size;
    let available = (free_pages + inactive_pages) * page_size;
    let used = total.saturating_sub(available);

    let percent = if total > 0 {
        (used as f64 / total as f64) * 100.0
    } else {
        0.0
    };

    // Swap
    let (swap_total, swap_used, swap_free) = if std::env::consts::OS == "freebsd" {
        parse_swapinfo()
    } else {
        parse_swapctl()
    };

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

fn parse_swapinfo() -> (u64, u64, u64) {
    let output = std::process::Command::new("swapinfo").args(["-k"]).output().ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    let mut total: u64 = 0;
    let mut used: u64 = 0;

    for line in stdout.lines().skip(1) {
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 3 {
            total += parts[1].parse::<u64>().unwrap_or(0) * 1024;
            used += parts[2].parse::<u64>().unwrap_or(0) * 1024;
        }
    }

    (total, used, total.saturating_sub(used))
}

fn parse_swapctl() -> (u64, u64, u64) {
    let output = std::process::Command::new("swapctl").args(["-sk"]).output().ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    // "total: 1048576 1k-blocks allocated, 0 used, 1048576 available"
    let mut total: u64 = 0;
    let mut used: u64 = 0;

    for line in stdout.lines() {
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 7 {
            total = parts[1].parse::<u64>().unwrap_or(0) * 1024;
            used = parts[3].parse::<u64>().unwrap_or(0) * 1024;
        }
    }

    (total, used, total.saturating_sub(used))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_swapinfo_empty() {
        let _ = parse_swapinfo();
    }
}
