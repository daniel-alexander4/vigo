// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Hardware info for illumos — uses `psrinfo` and `prtconf`.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let cpu_count = parse_psrinfo_count();
    let memory_mb = parse_prtconf_memory_mb();
    let model = parse_cpu_model();
    let disk_gb = collect_disk_sizes();

    Ok(json!({
        "cpu_count": cpu_count,
        "memory_mb": memory_mb,
        "model": model,
        "disk_gb": disk_gb,
    }))
}

fn parse_psrinfo_count() -> u64 {
    std::process::Command::new("psrinfo")
        .args(["-p"])
        .output()
        .ok()
        .filter(|o| o.status.success())
        .and_then(|o| {
            String::from_utf8_lossy(&o.stdout)
                .trim()
                .parse()
                .ok()
        })
        .unwrap_or(0)
}

fn parse_prtconf_memory_mb() -> u64 {
    // prtconf outputs "Memory size: 16384 Megabytes"
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

fn parse_cpu_model() -> String {
    // kstat -p cpu_info:0:cpu_info0:brand
    std::process::Command::new("kstat")
        .args(["-p", "cpu_info:0:cpu_info0:brand"])
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| {
            let stdout = String::from_utf8_lossy(&o.stdout);
            // Output: "cpu_info:0:cpu_info0:brand\tIntel(R) Xeon(R)..."
            stdout.split('\t').nth(1).unwrap_or("").trim().to_string()
        })
        .unwrap_or_default()
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
