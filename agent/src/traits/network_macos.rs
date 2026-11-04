// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};
use std::net::UdpSocket;

pub fn collect() -> Result<Value> {
    let hostname = hostname::get()
        .map(|h| h.to_string_lossy().to_string())
        .unwrap_or_default();

    let (ips, macs) = collect_interfaces();

    Ok(json!({
        "hostname": hostname,
        "fqdn": hostname.clone(),
        "ip_addresses": ips,
        "mac_addresses": macs,
    }))
}

fn collect_interfaces() -> (Vec<String>, Vec<String>) {
    let mut ips = Vec::new();
    let mut macs = Vec::new();

    // Primary IP via UDP trick.
    if let Ok(socket) = UdpSocket::bind("0.0.0.0:0") {
        if socket.connect("8.8.8.8:80").is_ok() {
            if let Ok(addr) = socket.local_addr() {
                ips.push(addr.ip().to_string());
            }
        }
    }

    // Parse ifconfig output for additional IPs and MACs.
    if let Ok(output) = crate::traits::run_cmd("ifconfig", &[]) {
        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines() {
            let trimmed = line.trim();
            if trimmed.starts_with("inet ") {
                // inet 192.168.1.100 netmask ...
                if let Some(ip) = trimmed.split_whitespace().nth(1) {
                    if ip != "127.0.0.1" && !ips.contains(&ip.to_string()) {
                        ips.push(ip.to_string());
                    }
                }
            } else if trimmed.starts_with("ether ") {
                if let Some(mac) = trimmed.split_whitespace().nth(1) {
                    if mac != "00:00:00:00:00:00" && !macs.contains(&mac.to_string()) {
                        macs.push(mac.to_string());
                    }
                }
            }
        }
    }

    (ips, macs)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_interfaces_no_panic() {
        let _ = collect_interfaces();
    }
}
