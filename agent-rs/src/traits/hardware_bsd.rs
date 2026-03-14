// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Hardware info for BSD — uses sysctl.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let cpu_count = super::sysctl_util::sysctl_u64("hw.ncpu").unwrap_or(0);

    // hw.physmem on FreeBSD/OpenBSD/NetBSD
    let memory_bytes = super::sysctl_util::sysctl_u64("hw.physmem").unwrap_or(0);
    let memory_mb = memory_bytes / (1024 * 1024);

    let model = super::sysctl_util::sysctl_string("hw.model");

    let disk_gb = collect_disk_sizes();

    Ok(json!({
        "cpu_count": cpu_count,
        "memory_mb": memory_mb,
        "model": model,
        "disk_gb": disk_gb,
    }))
}

fn collect_disk_sizes() -> Value {
    let mut map = serde_json::Map::new();
    if let Ok(output) = std::process::Command::new("df").args(["-k"]).output() {
        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines().skip(1) {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 6 {
                let mount = parts[parts.len() - 1];
                if mount.starts_with('/') {
                    if let Ok(kb) = parts[1].parse::<u64>() {
                        map.insert(mount.to_string(), json!(kb / (1024 * 1024)));
                    }
                }
            }
        }
    }
    Value::Object(map)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
