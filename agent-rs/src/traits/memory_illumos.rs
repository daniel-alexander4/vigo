// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Memory collector for illumos — uses `prtconf` + `kstat`.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let total_mb = parse_prtconf_memory_mb();
    let total = total_mb * 1024 * 1024;

    let page_size = parse_pagesize();
    let freemem_pages = kstat_u64("unix:0:system_pages:freemem");
    let free = freemem_pages * page_size;
    let used = total.saturating_sub(free);

    let percent = if total > 0 {
        (used as f64 / total as f64) * 100.0
    } else {
        0.0
    };

    let (swap_total, swap_used, swap_free) = parse_swap();
    let swap_percent = if swap_total > 0 {
        (swap_used as f64 / swap_total as f64) * 100.0
    } else {
        0.0
    };

    Ok(json!({
        "total": total,
        "available": free,
        "used": used,
        "free": free,
        "percent": (percent * 100.0).round() / 100.0,
        "swap_total": swap_total,
        "swap_used": swap_used,
        "swap_free": swap_free,
        "swap_percent": (swap_percent * 100.0).round() / 100.0,
    }))
}

fn parse_prtconf_memory_mb() -> u64 {
    std::process::Command::new("prtconf")
        .output()
        .ok()
        .filter(|o| o.status.success())
        .and_then(|o| {
            let stdout = String::from_utf8_lossy(&o.stdout);
            for line in stdout.lines() {
                if line.starts_with("Memory size:") {
                    return line.split_whitespace()
                        .nth(2)
                        .and_then(|n| n.parse().ok());
                }
            }
            None
        })
        .unwrap_or(0)
}

fn parse_pagesize() -> u64 {
    std::process::Command::new("pagesize")
        .output()
        .ok()
        .filter(|o| o.status.success())
        .and_then(|o| {
            String::from_utf8_lossy(&o.stdout)
                .trim()
                .parse()
                .ok()
        })
        .unwrap_or(4096)
}

fn kstat_u64(stat: &str) -> u64 {
    std::process::Command::new("kstat")
        .args(["-p", stat])
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

fn parse_swap() -> (u64, u64, u64) {
    // swap -l output:
    // swapfile             dev    swaplo  blocks    free
    // /dev/zvol/dsk/...    ...    ...     2097152   2097152
    let output = std::process::Command::new("swap").args(["-l"]).output().ok();
    let stdout = output.as_ref()
        .map(|o| String::from_utf8_lossy(&o.stdout).to_string())
        .unwrap_or_default();

    let mut total: u64 = 0;
    let mut free: u64 = 0;

    for line in stdout.lines().skip(1) {
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 5 {
            // blocks and free are in 512-byte blocks
            total += parts[3].parse::<u64>().unwrap_or(0) * 512;
            free += parts[4].parse::<u64>().unwrap_or(0) * 512;
        }
    }

    let used = total.saturating_sub(free);
    (total, used, free)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
