// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

#![cfg(windows)]

//! Windows network trait collector.
//!
//! Gathers hostname (cross-platform `hostname` crate), primary IP via UDP
//! socket trick, and MAC address via `ipconfig /all` output parsing.

use anyhow::Result;
use serde_json::{json, Value};
use std::net::UdpSocket;
use std::process::Command;

pub fn collect() -> Result<Value> {
    let hostname = hostname::get()
        .map(|h| h.to_string_lossy().to_string())
        .unwrap_or_default();

    let ip = get_outbound_ip().unwrap_or_default();
    let mac = get_mac_address().unwrap_or_default();

    Ok(json!({
        "hostname": hostname,
        "ip": ip,
        "mac": mac,
    }))
}

/// Determine the primary outbound IP by connecting a UDP socket to an
/// external address. The socket is never actually used to send data.
fn get_outbound_ip() -> Result<String> {
    let socket = UdpSocket::bind("0.0.0.0:0")?;
    socket.connect("8.8.8.8:80")?;
    Ok(socket.local_addr()?.ip().to_string())
}

/// Parse the first Physical Address from `ipconfig /all` output.
fn get_mac_address() -> Option<String> {
    let output = Command::new("ipconfig").arg("/all").output().ok()?;

    if !output.status.success() {
        return None;
    }

    let text = String::from_utf8_lossy(&output.stdout);
    parse_mac_from_ipconfig(&text)
}

/// Extract the first non-empty Physical Address (MAC) from ipconfig output.
/// Windows ipconfig uses "Physical Address. . . . . . . . . : XX-XX-XX-XX-XX-XX".
fn parse_mac_from_ipconfig(output: &str) -> Option<String> {
    for line in output.lines() {
        let line = line.trim();
        if let Some(rest) = line
            .strip_prefix("Physical Address")
            .or_else(|| line.strip_prefix("Physische Adresse"))
        {
            // Skip the dots and colon separator.
            if let Some(mac) = rest.rsplit(':').next() {
                let mac = mac.trim();
                if !mac.is_empty() && mac != "00-00-00-00-00-00" {
                    // Normalize to colon-separated lowercase.
                    return Some(mac.replace('-', ":").to_lowercase());
                }
            }
        }
    }
    None
}
