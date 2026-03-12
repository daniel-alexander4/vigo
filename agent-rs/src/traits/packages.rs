// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let installed = collect_packages();
    Ok(json!({ "installed": installed }))
}

fn collect_packages() -> Value {
    // Try dpkg (Debian/Ubuntu).
    if let Some(pkgs) = dpkg_packages() {
        return pkgs;
    }

    // Try rpm (RHEL/Fedora/SUSE).
    if let Some(pkgs) = rpm_packages() {
        return pkgs;
    }

    // Try brew (macOS).
    if let Some(pkgs) = brew_packages() {
        return pkgs;
    }

    json!({})
}

fn dpkg_packages() -> Option<Value> {
    let output = Command::new("dpkg-query")
        .args(["-W", "-f", "${Package}\\t${Version}\\n"])
        .output()
        .ok()?;

    if !output.status.success() {
        return None;
    }

    Some(parse_tab_separated(&String::from_utf8_lossy(&output.stdout)))
}

fn rpm_packages() -> Option<Value> {
    let output = Command::new("rpm")
        .args(["-qa", "--queryformat", "%{NAME}\\t%{VERSION}-%{RELEASE}\\n"])
        .output()
        .ok()?;

    if !output.status.success() {
        return None;
    }

    Some(parse_tab_separated(&String::from_utf8_lossy(&output.stdout)))
}

fn brew_packages() -> Option<Value> {
    let output = Command::new("brew")
        .args(["list", "--versions"])
        .output()
        .ok()?;

    if !output.status.success() {
        return None;
    }

    let mut map = serde_json::Map::new();
    for line in String::from_utf8_lossy(&output.stdout).lines() {
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 2 {
            map.insert(parts[0].to_string(), json!(parts[1]));
        }
    }
    Some(Value::Object(map))
}

fn parse_tab_separated(output: &str) -> Value {
    let mut map = serde_json::Map::new();
    for line in output.lines() {
        if let Some((name, version)) = line.split_once('\t') {
            if !name.is_empty() {
                map.insert(name.to_string(), json!(version));
            }
        }
    }
    Value::Object(map)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_tab_separated() {
        let input = "bash\t5.1-6\ncurl\t7.81.0-1\n";
        let val = parse_tab_separated(input);
        let obj = val.as_object().unwrap();
        assert_eq!(obj["bash"], "5.1-6");
        assert_eq!(obj["curl"], "7.81.0-1");
    }

    #[test]
    fn test_parse_tab_separated_empty() {
        let val = parse_tab_separated("");
        assert_eq!(val.as_object().unwrap().len(), 0);
    }
}
