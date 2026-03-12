// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let content = std::fs::read_to_string("/proc/swaps").unwrap_or_default();
    let devices = parse_swaps(&content);
    let count = devices.len();

    Ok(json!({
        "count": count,
        "devices": devices,
    }))
}

fn parse_swaps(content: &str) -> Vec<Value> {
    let mut devices = Vec::new();

    for (i, line) in content.lines().enumerate() {
        // Skip header line.
        if i == 0 {
            continue;
        }
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() < 5 {
            continue;
        }

        let path = parts[0];
        let swap_type = parts[1];
        let size_kb: i64 = parts[2].parse().unwrap_or(0);
        let used_kb: i64 = parts[3].parse().unwrap_or(0);
        let priority: i32 = parts[4].parse().unwrap_or(0);

        devices.push(json!({
            "path": path,
            "type": swap_type,
            "size_kb": size_kb,
            "used_kb": used_kb,
            "priority": priority,
        }));
    }

    devices
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_swaps() {
        let content = r#"Filename				Type		Size		Used		Priority
/dev/dm-1                               partition	8388604		0		-2
"#;
        let devices = parse_swaps(content);
        assert_eq!(devices.len(), 1);
        assert_eq!(devices[0]["path"], "/dev/dm-1");
        assert_eq!(devices[0]["type"], "partition");
        assert_eq!(devices[0]["size_kb"], 8388604);
        assert_eq!(devices[0]["used_kb"], 0);
        assert_eq!(devices[0]["priority"], -2);
    }

    #[test]
    fn test_parse_swaps_empty() {
        let devices = parse_swaps("Filename\tType\tSize\tUsed\tPriority\n");
        assert!(devices.is_empty());
    }
}
