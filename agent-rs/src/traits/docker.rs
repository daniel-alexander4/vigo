// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let installed = which("docker");

    if !installed {
        return Ok(json!({
            "installed": false,
            "version": "",
            "running_count": 0,
            "running": [],
            "storage_driver": "",
            "cgroup_driver": "",
        }));
    }

    let version = run_docker(&["version", "--format", "{{.Server.Version}}"]);
    let storage_driver = run_docker(&["info", "--format", "{{.Driver}}"]);
    let cgroup_driver = run_docker(&["info", "--format", "{{.CgroupDriver}}"]);

    let running = collect_running_containers();
    let running_count = running.len();

    Ok(json!({
        "installed": true,
        "version": version,
        "running_count": running_count,
        "running": running,
        "storage_driver": storage_driver,
        "cgroup_driver": cgroup_driver,
    }))
}

fn collect_running_containers() -> Vec<String> {
    let output = match Command::new("docker")
        .args(["ps", "--format", "{{.Names}}"])
        .output()
    {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
        _ => return vec![],
    };

    output
        .lines()
        .filter(|l| !l.is_empty())
        .map(String::from)
        .collect()
}

fn run_docker(args: &[&str]) -> String {
    Command::new("docker")
        .args(args)
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_default()
}

fn which(cmd: &str) -> bool {
    Command::new("which")
        .arg(cmd)
        .output()
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
        assert!(obj.contains_key("installed"));
        assert!(obj.contains_key("version"));
        assert!(obj.contains_key("running_count"));
        assert!(obj.contains_key("running"));
    }
}
