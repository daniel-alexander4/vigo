// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Listening ports trait collector.
//!
//! On Linux: parses /proc/net/tcp and /proc/net/tcp6 for LISTEN state entries.
//! On BSD/illumos: runs `sockstat -l` and parses output.

use anyhow::Result;
use serde_json::{json, Value};
use std::collections::BTreeSet;

pub fn collect() -> Result<Value> {
    let ports = collect_listening_ports();
    let sorted: Vec<u16> = ports.into_iter().collect();
    Ok(json!({ "listening": sorted }))
}

#[cfg(target_os = "linux")]
fn collect_listening_ports() -> BTreeSet<u16> {
    let mut ports = BTreeSet::new();
    parse_proc_net_tcp("/proc/net/tcp", &mut ports);
    parse_proc_net_tcp("/proc/net/tcp6", &mut ports);
    ports
}

#[cfg(target_os = "linux")]
fn parse_proc_net_tcp(path: &str, ports: &mut BTreeSet<u16>) {
    let content = match std::fs::read_to_string(path) {
        Ok(c) => c,
        Err(_) => return,
    };

    for line in content.lines().skip(1) {
        // Skip header line
        let fields: Vec<&str> = line.split_whitespace().collect();
        if fields.len() < 4 {
            continue;
        }

        // Field 3 (index 3) is the state. 0A = LISTEN.
        if fields[3] != "0A" {
            continue;
        }

        // Field 1 (index 1) is local_address in format "ADDR:PORT" (hex).
        if let Some(port_hex) = fields[1].rsplit(':').next() {
            if let Ok(port) = u16::from_str_radix(port_hex, 16) {
                if port > 0 {
                    ports.insert(port);
                }
            }
        }
    }
}

#[cfg(any(
    target_os = "macos",
    target_os = "freebsd",
    target_os = "openbsd",
    target_os = "netbsd",
    target_os = "illumos"
))]
fn collect_listening_ports() -> BTreeSet<u16> {
    use std::process::Command;

    let mut ports = BTreeSet::new();

    // Try sockstat -l (FreeBSD, illumos).
    if let Ok(output) = Command::new("sockstat").args(["-l"]).output() {
        if output.status.success() {
            let stdout = String::from_utf8_lossy(&output.stdout);
            for line in stdout.lines().skip(1) {
                let fields: Vec<&str> = line.split_whitespace().collect();
                // sockstat format varies, but local address is typically field 5 or 6.
                // Look for ":port" pattern in each field.
                for field in &fields {
                    if let Some(port_str) = field.rsplit(':').next() {
                        if let Ok(port) = port_str.parse::<u16>() {
                            if port > 0 {
                                ports.insert(port);
                            }
                        }
                    }
                }
            }
            return ports;
        }
    }

    // Fallback: try netstat -tlnp.
    if let Ok(output) = Command::new("netstat").args(["-tln"]).output() {
        if output.status.success() {
            let stdout = String::from_utf8_lossy(&output.stdout);
            for line in stdout.lines() {
                let fields: Vec<&str> = line.split_whitespace().collect();
                if fields.len() >= 4 {
                    // Local address is typically field 3 (index 3).
                    if let Some(port_str) = fields[3].rsplit(':').next() {
                        if let Ok(port) = port_str.parse::<u16>() {
                            if port > 0 {
                                ports.insert(port);
                            }
                        }
                    }
                }
            }
        }
    }

    ports
}

#[cfg(not(any(
    target_os = "linux",
    target_os = "macos",
    target_os = "freebsd",
    target_os = "openbsd",
    target_os = "netbsd",
    target_os = "illumos"
)))]
fn collect_listening_ports() -> BTreeSet<u16> {
    BTreeSet::new()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_listening_key() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("listening"));
        assert!(obj["listening"].is_array());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn test_parse_proc_net_tcp_listen() {
        let mut ports = BTreeSet::new();
        // Create a fake /proc/net/tcp style content.
        let content = "\
  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 00000000:01BB 00000000:0000 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
";
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("tcp");
        std::fs::write(&path, content).unwrap();
        parse_proc_net_tcp(path.to_str().unwrap(), &mut ports);

        // 0x0016 = 22 (SSH), state 0A = LISTEN.
        assert!(ports.contains(&22));
        // 0x01BB = 443, state 01 = ESTABLISHED — should NOT be included.
        assert!(!ports.contains(&443));
    }
}
