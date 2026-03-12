// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let cpu_count = count_logical_cpus();
    let memory_mb = total_memory_mb();
    let disk_gb = collect_disk_gb();

    Ok(json!({
        "cpu_count": cpu_count,
        "memory_mb": memory_mb,
        "disk_gb": disk_gb,
    }))
}

fn count_logical_cpus() -> usize {
    std::fs::read_to_string("/proc/cpuinfo")
        .map(|content| {
            content
                .lines()
                .filter(|line| line.starts_with("processor"))
                .count()
        })
        .unwrap_or(1)
}

fn total_memory_mb() -> u64 {
    std::fs::read_to_string("/proc/meminfo")
        .ok()
        .and_then(|content| {
            for line in content.lines() {
                if let Some(rest) = line.strip_prefix("MemTotal:") {
                    let rest = rest.trim();
                    if let Some(kb_str) = rest.strip_suffix("kB").or_else(|| rest.strip_suffix("KB")) {
                        return kb_str.trim().parse::<u64>().ok().map(|kb| kb / 1024);
                    }
                }
            }
            None
        })
        .unwrap_or(0)
}

fn collect_disk_gb() -> Value {
    let mut disks = serde_json::Map::new();
    let mounts = super::mountpoints::physical_mounts();

    for (mountpoint, _, _) in &mounts {
        if let Ok(stat) = nix::sys::statvfs::statvfs(mountpoint.as_str()) {
            let total_gb = (stat.blocks() * stat.fragment_size() as u64) / (1024 * 1024 * 1024);
            disks.insert(mountpoint.clone(), json!(total_gb));
        }
    }

    Value::Object(disks)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_count_logical_cpus() {
        let count = count_logical_cpus();
        assert!(count >= 1);
    }

    #[test]
    fn test_total_memory_mb() {
        let mb = total_memory_mb();
        // Any Linux system should have > 0 MB of memory.
        if std::path::Path::new("/proc/meminfo").exists() {
            assert!(mb > 0);
        }
    }

    #[test]
    fn test_collect_returns_expected_keys() {
        if let Ok(val) = collect() {
            let obj = val.as_object().unwrap();
            assert!(obj.contains_key("cpu_count"));
            assert!(obj.contains_key("memory_mb"));
            assert!(obj.contains_key("disk_gb"));
        }
    }
}
