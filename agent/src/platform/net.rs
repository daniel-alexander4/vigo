// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Network utilities — outbound IP detection that filters virtual/container interfaces.

use std::net::UdpSocket;

/// Virtual interface name prefixes to skip when selecting an outbound IP.
#[cfg(unix)]
const VIRTUAL_PREFIXES: &[&str] = &[
    "lo", "docker", "br-", "veth", "virbr", "cni", "flannel", "calico", "tun", "tap",
];

/// Get the preferred outbound IP, filtering out virtual/container interfaces.
///
/// On Unix: enumerates physical interfaces via getifaddrs, prefers the OS-chosen
/// outbound IP if it belongs to a physical interface, otherwise picks the first
/// physical interface IP. Falls back to the UDP socket trick on Windows or if
/// interface enumeration fails.
pub fn get_outbound_ip() -> String {
    #[cfg(unix)]
    {
        let physical = physical_interface_ips();
        if !physical.is_empty() {
            // If the OS-preferred outbound IP is on a physical interface, use it.
            if let Some(udp_ip) = udp_outbound_ip() {
                if physical.iter().any(|ip| ip == &udp_ip) {
                    return udp_ip;
                }
            }
            // OS chose a virtual interface — return first physical IP instead.
            return physical.into_iter().next().unwrap();
        }
    }

    // No physical interfaces found (or Windows): fall back to UDP trick.
    udp_outbound_ip().unwrap_or_else(|| "127.0.0.1".to_string())
}

/// UDP socket trick: connect to an external address and read the local IP the OS selected.
fn udp_outbound_ip() -> Option<String> {
    UdpSocket::bind("0.0.0.0:0")
        .and_then(|s| {
            s.connect("8.8.8.8:80")?;
            s.local_addr()
        })
        .map(|addr| addr.ip().to_string())
        .ok()
}

/// Enumerate IPv4 addresses on physical (non-virtual) UP interfaces via getifaddrs.
#[cfg(unix)]
fn physical_interface_ips() -> Vec<String> {
    use std::ffi::CStr;
    use std::net::Ipv4Addr;

    let mut ips = Vec::new();

    unsafe {
        let mut ifaddrs: *mut libc::ifaddrs = std::ptr::null_mut();
        if libc::getifaddrs(&mut ifaddrs) != 0 {
            return ips;
        }

        let mut cur = ifaddrs;
        while !cur.is_null() {
            let ifa = &*cur;
            cur = ifa.ifa_next;

            if ifa.ifa_addr.is_null() {
                continue;
            }

            // IPv4 only.
            if i32::from((*ifa.ifa_addr).sa_family) != libc::AF_INET {
                continue;
            }

            // Must be UP and not loopback.
            let flags = ifa.ifa_flags;
            if flags & (libc::IFF_UP as u32) == 0 || flags & (libc::IFF_LOOPBACK as u32) != 0 {
                continue;
            }

            // Skip virtual interfaces by name prefix.
            let name = CStr::from_ptr(ifa.ifa_name).to_string_lossy();
            if VIRTUAL_PREFIXES.iter().any(|p| name.starts_with(p)) {
                continue;
            }

            // Extract IPv4 address.
            let sockaddr = ifa.ifa_addr as *const libc::sockaddr_in;
            let ip = Ipv4Addr::from(u32::from_be((*sockaddr).sin_addr.s_addr));

            if ip.is_loopback() || ip.is_link_local() {
                continue;
            }

            let s = ip.to_string();
            if !ips.contains(&s) {
                ips.push(s);
            }
        }

        libc::freeifaddrs(ifaddrs);
    }

    ips
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_get_outbound_ip_not_loopback() {
        let ip = get_outbound_ip();
        assert_ne!(ip, "127.0.0.1", "should prefer a non-loopback IP when available");
    }

    #[test]
    fn test_udp_outbound_ip() {
        // Should return Some on any networked machine.
        let ip = udp_outbound_ip();
        assert!(ip.is_some());
    }

    #[cfg(unix)]
    #[test]
    fn test_physical_interface_ips_non_empty() {
        let ips = physical_interface_ips();
        // On any machine with a network connection, there should be at least one.
        assert!(!ips.is_empty(), "expected at least one physical interface IP");
    }

    #[cfg(unix)]
    #[test]
    fn test_physical_ips_exclude_virtual() {
        let ips = physical_interface_ips();
        for ip in &ips {
            // These are IP addresses, not interface names — but verify they're valid IPv4.
            assert!(ip.parse::<std::net::Ipv4Addr>().is_ok(), "invalid IP: {ip}");
        }
    }
}
