// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let content = std::fs::read_to_string("/proc/loadavg").unwrap_or_default();
    Ok(parse_loadavg(&content))
}

fn parse_loadavg(content: &str) -> Value {
    let parts: Vec<&str> = content.split_whitespace().collect();
    let load1 = parts.first().and_then(|s| s.parse::<f64>().ok()).unwrap_or(0.0);
    let load5 = parts.get(1).and_then(|s| s.parse::<f64>().ok()).unwrap_or(0.0);
    let load15 = parts.get(2).and_then(|s| s.parse::<f64>().ok()).unwrap_or(0.0);

    json!({
        "load1": load1,
        "load5": load5,
        "load15": load15,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_loadavg() {
        let val = parse_loadavg("0.50 1.25 2.00 1/234 5678\n");
        assert_eq!(val["load1"], 0.5);
        assert_eq!(val["load5"], 1.25);
        assert_eq!(val["load15"], 2.0);
    }

    #[test]
    fn test_parse_loadavg_empty() {
        let val = parse_loadavg("");
        assert_eq!(val["load1"], 0.0);
    }
}
