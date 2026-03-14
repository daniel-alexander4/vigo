// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let mounts = super::mountpoints::physical_mounts();
    let mut result = serde_json::Map::new();

    for (mountpoint, _, _) in &mounts {
        if let Ok(stat) = nix::sys::statvfs::statvfs(mountpoint.as_str()) {
            let block_size = stat.fragment_size() as u64;
            let total = stat.blocks() * block_size;
            let free = stat.blocks_free() * block_size;
            let used = total.saturating_sub(free);

            let percent = if total > 0 {
                (used as f64 / total as f64) * 100.0
            } else {
                0.0
            };

            result.insert(
                mountpoint.clone(),
                json!({
                    "total": total,
                    "used": used,
                    "free": free,
                    "percent": (percent * 100.0).round() / 100.0,
                }),
            );
        }
    }

    Ok(Value::Object(result))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_object() {
        if let Ok(val) = collect() {
            assert!(val.is_object());
        }
    }
}
