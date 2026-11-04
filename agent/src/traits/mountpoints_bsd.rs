// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Mountpoints collector for macOS and BSD — parses `mount` output.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let mounts = physical_mounts();
    let result: Vec<Value> = mounts.iter().map(|(device, mountpoint, fstype)| {
        json!({
            "device": device,
            "mountpoint": mountpoint,
            "fstype": fstype,
        })
    }).collect();

    Ok(json!(result))
}

pub fn physical_mounts() -> Vec<(String, String, String)> {
    let mut mounts = Vec::new();
    let output = crate::traits::run_cmd("mount", &[]).ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    for line in stdout.lines() {
        // Format: /dev/disk1s1 on / (apfs, local, journaled)
        if let Some((device_part, rest)) = line.split_once(" on ") {
            if let Some((mountpoint, type_part)) = rest.split_once(" (") {
                let fstype = type_part.split(',').next().unwrap_or("").trim().trim_end_matches(')').to_string();
                let device = device_part.trim().to_string();
                if device.starts_with("/dev/") {
                    mounts.push((device, mountpoint.trim().to_string(), fstype));
                }
            }
        }
    }

    mounts
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_physical_mounts_no_panic() {
        let _ = physical_mounts();
    }
}
