// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

use anyhow::Result;
use serde_json::{json, Value};

/// Pseudo filesystem types to exclude when listing physical mounts.
const PSEUDO_FS: &[&str] = &[
    "proc", "sysfs", "devtmpfs", "devpts", "tmpfs", "cgroup", "cgroup2",
    "pstore", "debugfs", "securityfs", "configfs", "fusectl", "mqueue",
    "hugetlbfs", "autofs", "binfmt_misc", "tracefs", "overlay", "squashfs",
    "nsfs", "ramfs", "efivarfs", "bpf", "rpc_pipefs", "nfsd", "fuse.lxcfs",
    "fuse.snapfuse",
];

pub fn collect() -> Result<Value> {
    let mounts = physical_mounts();
    let mut result = serde_json::Map::new();

    for (mountpoint, device, fstype) in &mounts {
        if let Ok(stat) = nix::sys::statvfs::statvfs(mountpoint.as_str()) {
            let block_size = stat.fragment_size() as u64;
            let total = stat.blocks() * block_size;
            let free = stat.blocks_free() * block_size;
            let avail = stat.blocks_available() * block_size;
            let used = total.saturating_sub(free);

            let total_gb = total / (1024 * 1024 * 1024);
            let used_gb = used / (1024 * 1024 * 1024);
            let avail_gb = avail / (1024 * 1024 * 1024);

            let used_pct = if total > 0 {
                (used as f64 / total as f64) * 100.0
            } else {
                0.0
            };

            let inode_total = stat.files();
            let inode_free = stat.files_free();
            let inode_pct = if inode_total > 0 {
                ((inode_total - inode_free) as f64 / inode_total as f64) * 100.0
            } else {
                0.0
            };

            result.insert(
                mountpoint.clone(),
                json!({
                    "device": device,
                    "fstype": fstype,
                    "total_gb": total_gb,
                    "used_gb": used_gb,
                    "avail_gb": avail_gb,
                    "used_pct": (used_pct * 100.0).round() / 100.0,
                    "inode_pct": (inode_pct * 100.0).round() / 100.0,
                }),
            );
        }
    }

    Ok(json!({ "mounts": result }))
}

/// Parse /proc/mounts and return physical mount entries: (mountpoint, device, fstype).
pub(crate) fn physical_mounts() -> Vec<(String, String, String)> {
    let content = std::fs::read_to_string("/proc/mounts").unwrap_or_default();
    parse_mounts(&content)
}

fn parse_mounts(content: &str) -> Vec<(String, String, String)> {
    let mut result = Vec::new();
    for line in content.lines() {
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() < 3 {
            continue;
        }
        let device = parts[0];
        let mountpoint = parts[1];
        let fstype = parts[2];

        if PSEUDO_FS.contains(&fstype) {
            continue;
        }
        // Skip entries that don't start with / (e.g., "none", "sunrpc").
        if !device.starts_with('/') {
            continue;
        }

        result.push((
            mountpoint.to_string(),
            device.to_string(),
            fstype.to_string(),
        ));
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_mounts() {
        let content = r#"/dev/sda1 / ext4 rw,relatime 0 0
proc /proc proc rw,nosuid 0 0
tmpfs /tmp tmpfs rw 0 0
/dev/sdb1 /data xfs rw,relatime 0 0
"#;
        let mounts = parse_mounts(content);
        assert_eq!(mounts.len(), 2);
        assert_eq!(mounts[0].0, "/");
        assert_eq!(mounts[0].1, "/dev/sda1");
        assert_eq!(mounts[0].2, "ext4");
        assert_eq!(mounts[1].0, "/data");
    }

    #[test]
    fn test_parse_mounts_filters_pseudo() {
        let content = "sysfs /sys sysfs rw 0 0\ndevtmpfs /dev devtmpfs rw 0 0\n";
        let mounts = parse_mounts(content);
        assert!(mounts.is_empty());
    }
}
