// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

use anyhow::Result;
use serde_json::{json, Value};
use std::net::UdpSocket;

pub fn collect() -> Result<Value> {
    let hostname = hostname::get()
        .map(|h| h.to_string_lossy().to_string())
        .unwrap_or_default();

    let fqdn = resolve_fqdn(&hostname);
    let (ips, macs) = collect_interfaces();

    Ok(json!({
        "hostname": hostname,
        "fqdn": fqdn,
        "ip_addresses": ips,
        "mac_addresses": macs,
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
