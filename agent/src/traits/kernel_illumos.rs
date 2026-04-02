// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Kernel info for illumos — uses `uname`.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let version = run_cmd("uname", &["-v"]);
    let release = run_cmd("uname", &["-r"]);

    Ok(json!({
        "version": version,
        "release": release,
    }))
}

fn run_cmd(cmd: &str, args: &[&str]) -> String {
    std::process::Command::new(cmd)
        .args(args)
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
