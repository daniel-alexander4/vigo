// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let available = which("systemctl");

    if !available {
        return Ok(json!({
            "available": false,
            "version": "",
            "default_target": "",
            "failed_units": [],
            "failed_count": 0,
        }));
    }

    let version = run_cmd("systemctl", &["--version"])
        .and_then(|out| {
            // "systemd 249 (249.11-0ubuntu3.17)" → "249"
            out.lines()
                .next()
                .and_then(|line| line.split_whitespace().nth(1))
                .map(String::from)
        })
        .unwrap_or_default();

    let default_target = run_cmd("systemctl", &["get-default"])
        .map(|s| s.trim().to_string())
        .unwrap_or_default();

    let failed_units = collect_failed_units();
    let failed_count = failed_units.len();

    Ok(json!({
        "available": true,
        "version": version,
        "default_target": default_target,
        "failed_units": failed_units,
        "failed_count": failed_count,
    }))
}

fn collect_failed_units() -> Vec<String> {
    let output = match crate::traits::run_cmd("systemctl", &["--state=failed", "list-units", "--no-legend", "--plain"])
    {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
        _ => return vec![],
    };

    output
        .lines()
        .filter_map(|line| {
            let unit = line.split_whitespace().next()?;
            if unit.is_empty() {
                None
            } else {
                Some(unit.to_string())
            }
        })
        .collect()
}

fn run_cmd(cmd: &str, args: &[&str]) -> Option<String> {
    crate::traits::run_cmd(cmd, args)
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).to_string())
}

fn which(cmd: &str) -> bool {
    crate::traits::run_cmd("which", &[cmd])
        .map(|o| o.status.success())
        .unwrap_or(false)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_expected_keys() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("available"));
        assert!(obj.contains_key("version"));
        assert!(obj.contains_key("default_target"));
        assert!(obj.contains_key("failed_units"));
        assert!(obj.contains_key("failed_count"));
    }
}
