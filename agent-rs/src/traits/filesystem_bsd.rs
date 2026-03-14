// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Filesystem collector for macOS and BSD — uses statvfs via nix.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let mounts = super::mountpoints_bsd::physical_mounts();
    let mut filesystems = serde_json::Map::new();

    for (device, mountpoint, fstype) in &mounts {
        if let Ok(stat) = nix::sys::statvfs::statvfs(mountpoint.as_str()) {
            let total = stat.blocks() as u64 * stat.fragment_size() as u64;
            let free = stat.blocks_free() as u64 * stat.fragment_size() as u64;
            let avail = stat.blocks_available() as u64 * stat.fragment_size() as u64;
            let used = total.saturating_sub(free);
            let percent = if total > 0 {
                (used as f64 / total as f64) * 100.0
            } else {
                0.0
            };

            filesystems.insert(mountpoint.clone(), json!({
                "device": device,
                "fstype": fstype,
                "total": total,
                "used": used,
                "free": free,
                "available": avail,
                "percent": (percent * 100.0).round() / 100.0,
            }));
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
