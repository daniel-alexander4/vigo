// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Swap collector for macOS — uses `sysctl vm.swapusage`.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let raw = super::sysctl_util::sysctl_string("vm.swapusage");
    let (total, used, free) = parse_swap_usage(&raw);

    // Parse individual swap devices from sysctl
    let devices: Vec<Value> = Vec::new(); // macOS doesn't expose per-device swap info easily

    Ok(json!({
        "count": if total > 0 { 1 } else { 0 },
        "devices": devices,
        "total_kb": total / 1024,
        "used_kb": used / 1024,
        "free_kb": free / 1024,
    }))
}

fn parse_swap_usage(raw: &str) -> (u64, u64, u64) {
    if raw.is_empty() {
        return (0, 0, 0);
    }

    let mut total: u64 = 0;
    let mut used: u64 = 0;
    let mut free: u64 = 0;

    for part in raw.split("  ") {
        if let Some((key, val)) = part.trim().split_once(" = ") {
            let bytes = parse_size(val.trim());
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

fn parse_size(s: &str) -> u64 {
    if let Some(n) = s.strip_suffix('M') {
        n.parse::<f64>().map(|f| (f * 1024.0 * 1024.0) as u64).unwrap_or(0)
    } else if let Some(n) = s.strip_suffix('G') {
        n.parse::<f64>().map(|f| (f * 1024.0 * 1024.0 * 1024.0) as u64).unwrap_or(0)
    } else {
        s.parse::<u64>().unwrap_or(0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_swap_usage() {
        let (t, u, f) = parse_swap_usage("total = 2048.00M  used = 100.50M  free = 1947.50M");
        assert!(t > 0);
        assert_eq!(t, (2048.0 * 1024.0 * 1024.0) as u64);
        assert_eq!(u, (100.5 * 1024.0 * 1024.0) as u64);
        assert!(f > 0);
    }

    #[test]
    fn test_parse_swap_empty() {
        let (t, u, f) = parse_swap_usage("");
        assert_eq!(t, 0);
        assert_eq!(u, 0);
        assert_eq!(f, 0);
    }
}
