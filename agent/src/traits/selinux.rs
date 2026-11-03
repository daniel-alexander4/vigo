// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    // SELinux config must exist.
    if !std::path::Path::new("/etc/selinux/config").exists() {
        return Ok(json!({
            "enabled": false,
            "mode": "",
            "policy": "",
        }));
    }

    let mode = Command::new("getenforce")
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_lowercase())
        .unwrap_or_default();

    let enabled = !mode.is_empty() && mode != "disabled";

    let policy = get_policy();

    Ok(json!({
        "enabled": enabled,
        "mode": mode,
        "policy": policy,
    }))
}

fn get_policy() -> String {
    let output = match Command::new("sestatus").output() {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
        _ => return String::new(),
    };

    for line in output.lines() {
        let line = line.trim();
        if line.starts_with("Loaded policy name:") || line.starts_with("Policy from config file:") {
            if let Some((_, val)) = line.split_once(':') {
                return val.trim().to_string();
            }
        }
    }
    String::new()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_expected_keys() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("enabled"));
        assert!(obj.contains_key("mode"));
        assert!(obj.contains_key("policy"));
    }
}
