// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Mountpoints collector for illumos — parses `mount` output.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let mut mounts = Vec::new();

    if let Ok(output) = std::process::Command::new("mount").output() {
        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines() {
            // illumos mount output: "<device> on <mountpoint> type <fstype> ..."
            // or: "<device> <mountpoint> <fstype> ..."
            if let Some(entry) = parse_mount_line(line) {
                mounts.push(entry);
            }
        }
    }

    Ok(json!(mounts))
}

fn parse_mount_line(line: &str) -> Option<Value> {
    let parts: Vec<&str> = line.split_whitespace().collect();
    if parts.len() >= 3 {
        // Try "device on mountpoint type fstype" format first.
        if parts.len() >= 4 && parts[1] == "on" {
            let device = parts[0];
            let mountpoint = parts[2];
            let fstype = if parts.len() >= 5 && parts[3] == "type" {
                parts[4]
            } else {
                ""
            };
            if mountpoint.starts_with('/') {
                return Some(json!({
                    "device": device,
                    "mountpoint": mountpoint,
                    "fstype": fstype,
                }));
            }
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }

    #[test]
    fn test_parse_mount_line() {
        let line = "/dev/dsk/c1d0s0 on / type ufs read/write";
        let entry = parse_mount_line(line);
        assert!(entry.is_some());
        let v = entry.unwrap();
        assert_eq!(v["mountpoint"], "/");
        assert_eq!(v["fstype"], "ufs");
    }
}
