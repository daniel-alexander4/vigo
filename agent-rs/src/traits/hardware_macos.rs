// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let cpu_count = super::sysctl_util::sysctl_u64("hw.ncpu").unwrap_or(0);
    let memory_bytes = super::sysctl_util::sysctl_u64("hw.memsize").unwrap_or(0);
    let memory_mb = memory_bytes / (1024 * 1024);
    let cpu_brand = super::sysctl_util::sysctl_string("machdep.cpu.brand_string");

    // Disk info via df
    let disk_gb = collect_disk_sizes();

    Ok(json!({
        "cpu_count": cpu_count,
        "memory_mb": memory_mb,
        "cpu_brand": cpu_brand,
        "disk_gb": disk_gb,
    }))
}

fn collect_disk_sizes() -> Value {
    let mut map = serde_json::Map::new();
    let output = std::process::Command::new("df")
        .args(["-k"])
        .output()
        .ok();

    if let Some(o) = output {
        let stdout = String::from_utf8_lossy(&o.stdout);
        for line in stdout.lines().skip(1) {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 6 {
                let mount = parts[parts.len() - 1];
                if mount == "/" || mount.starts_with("/Volumes/") || mount.starts_with("/System/Volumes/Data") {
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
    fn test_collect_disk_sizes_no_panic() {
        let _ = collect_disk_sizes();
    }
}
