// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let content = std::fs::read_to_string("/proc/meminfo").unwrap_or_default();
    Ok(parse_meminfo(&content))
}

fn parse_meminfo(content: &str) -> Value {
    let mut total = 0u64;
    let mut free = 0u64;
    let mut available = 0u64;
    let mut buffers = 0u64;
    let mut cached = 0u64;
    let mut swap_total = 0u64;
    let mut swap_free = 0u64;

    for line in content.lines() {
        if let Some((key, rest)) = line.split_once(':') {
            let val_kb = parse_kb_value(rest.trim());
            match key.trim() {
                "MemTotal" => total = val_kb * 1024,
                "MemFree" => free = val_kb * 1024,
                "MemAvailable" => available = val_kb * 1024,
                "Buffers" => buffers = val_kb * 1024,
                "Cached" => cached = val_kb * 1024,
                "SwapTotal" => swap_total = val_kb * 1024,
                "SwapFree" => swap_free = val_kb * 1024,
                _ => {}
            }
        }
    }

    // If MemAvailable not present (old kernels), approximate.
    if available == 0 && total > 0 {
        available = free + buffers + cached;
    }

    let used = total.saturating_sub(available);
    let swap_used = swap_total.saturating_sub(swap_free);

    let percent = if total > 0 {
        (used as f64 / total as f64) * 100.0
    } else {
        0.0
    };

    let swap_percent = if swap_total > 0 {
        (swap_used as f64 / swap_total as f64) * 100.0
    } else {
        0.0
    };

    json!({
        "total": total,
        "available": available,
        "used": used,
        "free": free,
        "percent": (percent * 100.0).round() / 100.0,
        "swap_total": swap_total,
        "swap_used": swap_used,
        "swap_free": swap_free,
        "swap_percent": (swap_percent * 100.0).round() / 100.0,
    })
}

fn parse_kb_value(s: &str) -> u64 {
    s.split_whitespace()
        .next()
        .and_then(|v| v.parse().ok())
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_meminfo() {
        let content = r#"MemTotal:       16384000 kB
MemFree:         2048000 kB
MemAvailable:    8192000 kB
Buffers:          512000 kB
Cached:          4096000 kB
SwapTotal:       8192000 kB
SwapFree:        8192000 kB
"#;
        let val = parse_meminfo(content);
        assert_eq!(val["total"], 16384000u64 * 1024);
        assert_eq!(val["available"], 8192000u64 * 1024);
        assert_eq!(val["free"], 2048000u64 * 1024);
        assert_eq!(val["swap_total"], 8192000u64 * 1024);
        assert_eq!(val["swap_free"], 8192000u64 * 1024);
        assert_eq!(val["swap_used"], 0);
        assert_eq!(val["swap_percent"], 0.0);
    }

    #[test]
    fn test_parse_kb_value() {
        assert_eq!(parse_kb_value("16384000 kB"), 16384000);
        assert_eq!(parse_kb_value("0 kB"), 0);
        assert_eq!(parse_kb_value(""), 0);
    }
}
