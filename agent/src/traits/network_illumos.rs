// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Network info for illumos — uses `ipadm` and `dladm`.

use anyhow::Result;
use serde_json::{json, Value};
use std::net::UdpSocket;

pub fn collect() -> Result<Value> {
    let hostname = hostname::get()
        .map(|h| h.to_string_lossy().to_string())
        .unwrap_or_default();

    let ips = collect_ips();
    let macs = collect_macs();

    Ok(json!({
        "hostname": hostname,
        "fqdn": hostname.clone(),
        "ip_addresses": ips,
        "mac_addresses": macs,
    }))
}

fn collect_ips() -> Vec<String> {
    let mut ips = Vec::new();

    // Primary IP via UDP socket trick.
    if let Ok(socket) = UdpSocket::bind("0.0.0.0:0") {
        if socket.connect("8.8.8.8:80").is_ok() {
            if let Ok(addr) = socket.local_addr() {
                ips.push(addr.ip().to_string());
            }
        }
    }

    // ipadm show-addr -p -o ADDR
    if let Ok(output) = std::process::Command::new("ipadm")
        .args(["show-addr", "-p", "-o", "ADDR"])
        .output()
    {
        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines() {
            let addr = line.trim();
            // Strip CIDR prefix if present (e.g., "192.168.1.1/24" → "192.168.1.1").
            let ip = addr.split('/').next().unwrap_or(addr);
            if !ip.is_empty() && ip != "127.0.0.1" && ip != "::1" && !ips.contains(&ip.to_string()) {
                ips.push(ip.to_string());
            }
        }
    }

    ips
}

fn collect_macs() -> Vec<String> {
    let mut macs = Vec::new();

    // dladm show-phys -p -o ADDRESS
    if let Ok(output) = std::process::Command::new("dladm")
        .args(["show-phys", "-p", "-o", "ADDRESS"])
        .output()
    {
        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines() {
            let mac = line.trim();
            if !mac.is_empty() && mac != "00:00:00:00:00:00" && !macs.contains(&mac.to_string()) {
                macs.push(mac.to_string());
            }
        }
    }

    macs
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
