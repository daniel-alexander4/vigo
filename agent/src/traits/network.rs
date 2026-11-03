// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};
use std::net::UdpSocket;
use std::process::Command;

pub fn collect() -> Result<Value> {
    let hostname = hostname::get()
        .map(|h| h.to_string_lossy().to_string())
        .unwrap_or_default();

    let fqdn = resolve_fqdn(&hostname);
    let (ips, macs) = collect_interfaces();

    let interfaces = collect_interface_details();

    Ok(json!({
        "hostname": hostname,
        "fqdn": fqdn,
        "ip_addresses": ips,
        "mac_addresses": macs,
        "interfaces": interfaces,
    }))
}

fn resolve_fqdn(hostname: &str) -> String {
    // Try reverse DNS on the outbound IP.
    use std::net::ToSocketAddrs;
    if let Ok(addr) = format!("{}:0", hostname).to_socket_addrs() {
        for a in addr {
            if let Ok(names) = dns_lookup_addr(&a.ip()) {
                if let Some(name) = names.first() {
                    if name.contains('.') {
                        return name.trim_end_matches('.').to_string();
                    }
                }
            }
        }
    }
    hostname.to_string()
}

fn dns_lookup_addr(ip: &std::net::IpAddr) -> Result<Vec<String>> {
    // Use getaddrinfo reverse lookup via libc.
    // Simplified: just return hostname as fallback.
    let _ = ip;
    Ok(vec![])
}

fn collect_interfaces() -> (Vec<String>, Vec<String>) {
    let mut ips = Vec::new();
    let mut macs = Vec::new();

    // Collect IPs from interface addresses via /proc/net or UDP trick.
    if let Ok(ip) = get_outbound_ip() {
        ips.push(ip);
    }

    // Read additional IPs from /sys/class/net/*/
    if let Ok(entries) = std::fs::read_dir("/sys/class/net") {
        for entry in entries.flatten() {
            let name = entry.file_name().to_string_lossy().to_string();
            if name == "lo" {
                continue;
            }

            // Read MAC address.
            let mac_path = entry.path().join("address");
            if let Ok(mac) = std::fs::read_to_string(&mac_path) {
                let mac = mac.trim().to_string();
                if !mac.is_empty() && mac != "00:00:00:00:00:00" && !macs.contains(&mac) {
                    macs.push(mac);
                }
            }

            // Read IPv4 from /proc/net/fib_trie is complex; use operstate to filter.
            let operstate = entry.path().join("operstate");
            if let Ok(state) = std::fs::read_to_string(operstate) {
                if state.trim() != "up" {
                    continue;
                }
            }
        }
    }

    // Parse /proc/net/if_inet6 for IPv6 addresses.
    if let Ok(content) = std::fs::read_to_string("/proc/net/if_inet6") {
        for line in content.lines() {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 6 {
                let iface = parts[5];
                if iface == "lo" {
                    continue;
                }
                let hex = parts[0];
                if let Some(addr) = parse_ipv6_hex(hex) {
                    if !addr.starts_with("fe80:") && !ips.contains(&addr) {
                        ips.push(addr);
                    }
                }
            }
        }
    }

    // Ensure we have at least the primary IP.
    if ips.is_empty() {
        if let Ok(ip) = get_outbound_ip() {
            ips.push(ip);
        }
    }

    // Sort so both lists are byte-identical between refreshes regardless of
    // /sys/class/net enumeration order (ADR-033).
    ips.sort();
    macs.sort();

    (ips, macs)
}

fn get_outbound_ip() -> Result<String> {
    let socket = UdpSocket::bind("0.0.0.0:0")?;
    socket.connect("8.8.8.8:80")?;
    Ok(socket.local_addr()?.ip().to_string())
}

fn parse_ipv6_hex(hex: &str) -> Option<String> {
    if hex.len() != 32 {
        return None;
    }
    let groups: Vec<String> = (0..8)
        .map(|i| {
            let start = i * 4;
            hex[start..start + 4].to_string()
        })
        .collect();
    // Parse to normalize (remove leading zeros).
    let formatted = groups.join(":");
    formatted.parse::<std::net::Ipv6Addr>().ok().map(|a| a.to_string())
}

/// Collect per-interface firmware and driver details for physical NICs.
/// Uses sysfs first (zero cost), falls back to ethtool for firmware version.
fn collect_interface_details() -> serde_json::Map<String, Value> {
    let mut result = serde_json::Map::new();

    let entries = match std::fs::read_dir("/sys/class/net") {
        Ok(e) => e,
        Err(_) => return result,
    };

    for entry in entries.flatten() {
        let name = entry.file_name().to_string_lossy().to_string();

        // Skip loopback and virtual interfaces.
        if name == "lo" || name.starts_with("veth") || name.starts_with("docker")
            || name.starts_with("br-") || name.starts_with("virbr")
        {
            continue;
        }

        // Only collect for interfaces that are up.
        let operstate = entry.path().join("operstate");
        let is_up = std::fs::read_to_string(&operstate)
            .map(|s| s.trim() == "up")
            .unwrap_or(false);
        if !is_up {
            continue;
        }

        let mut iface = serde_json::Map::new();

        // Driver name from sysfs (free).
        if let Ok(driver_link) = std::fs::read_link(entry.path().join("device/driver")) {
            if let Some(driver_name) = driver_link.file_name() {
                iface.insert(
                    "driver".into(),
                    json!(driver_name.to_string_lossy().to_string()),
                );
            }
        }

        // Firmware version from ethtool -i (subprocess, only for up interfaces).
        // The per-collector 5s timeout in mod.rs caps total runtime.
        if let Ok(output) = Command::new("ethtool")
            .args(["-i", &name])
            .output()
        {
            if output.status.success() {
                let stdout = String::from_utf8_lossy(&output.stdout);
                for line in stdout.lines() {
                    if let Some((key, val)) = line.split_once(':') {
                        let key = key.trim();
                        let val = val.trim();
                        if val.is_empty() || val == "N/A" {
                            continue;
                        }
                        match key {
                            "driver" => {
                                iface.entry("driver").or_insert_with(|| json!(val));
                            }
                            "version" => {
                                iface.insert("driver_version".into(), json!(val));
                            }
                            "firmware-version" => {
                                iface.insert("firmware_version".into(), json!(val));
                            }
                            "bus-info" => {
                                iface.insert("bus_info".into(), json!(val));
                            }
                            _ => {}
                        }
                    }
                }
            }
        }

        if !iface.is_empty() {
            result.insert(name, Value::Object(iface));
        }
    }

    result
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_ipv6_hex() {
        assert_eq!(
            parse_ipv6_hex("20010db8000000000000000000000001"),
            Some("2001:db8::1".to_string())
        );
        assert_eq!(parse_ipv6_hex("short"), None);
    }

    #[test]
    fn test_collect_returns_expected_keys() {
        if let Ok(val) = collect() {
            let obj = val.as_object().unwrap();
            assert!(obj.contains_key("hostname"));
            assert!(obj.contains_key("fqdn"));
            assert!(obj.contains_key("ip_addresses"));
            assert!(obj.contains_key("mac_addresses"));
        }
    }
}
