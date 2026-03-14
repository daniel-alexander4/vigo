// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Filesystem collector for illumos — uses `df -k`.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let mut filesystems = serde_json::Map::new();

    if let Ok(output) = std::process::Command::new("df").args(["-k"]).output() {
        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines().skip(1) {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 6 {
                let mount = parts[parts.len() - 1];
                if mount.starts_with('/') {
                    let total_kb: u64 = parts[1].parse().unwrap_or(0);
                    let used_kb: u64 = parts[2].parse().unwrap_or(0);
                    let avail_kb: u64 = parts[3].parse().unwrap_or(0);
                    filesystems.insert(mount.to_string(), json!({
                        "total_bytes": total_kb * 1024,
                        "used_bytes": used_kb * 1024,
                        "available_bytes": avail_kb * 1024,
                    }));
                }
            }
        }
    }

    Ok(Value::Object(filesystems))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
