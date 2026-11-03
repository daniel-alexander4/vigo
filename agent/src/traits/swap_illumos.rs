// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Swap collector for illumos — uses `swap -l`.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let (total, used, free) = parse_swap();

    let percent = if total > 0 {
        (used as f64 / total as f64) * 100.0
    } else {
        0.0
    };

    Ok(json!({
        "total": total,
        "used": used,
        "free": free,
        "percent": (percent * 100.0).round() / 100.0,
    }))
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
