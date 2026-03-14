// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Load average collector for macOS and BSD — uses libc `getloadavg()`.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let mut loads = [0.0f64; 3];
    let n = unsafe { libc::getloadavg(loads.as_mut_ptr(), 3) };
    if n < 3 {
        // Fallback to sysctl
        let raw = super::sysctl_util::sysctl_string("vm.loadavg");
        let parts: Vec<f64> = raw.split_whitespace()
            .filter_map(|s| s.trim_matches(|c: char| !c.is_ascii_digit() && c != '.').parse().ok())
            .collect();
        if parts.len() >= 3 {
            loads[0] = parts[0];
            loads[1] = parts[1];
            loads[2] = parts[2];
        }
    }

    Ok(json!({
        "load1": loads[0],
        "load5": loads[1],
        "load15": loads[2],
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        // getloadavg may not work on all CI, but shouldn't panic
        let _ = collect();
    }
}
