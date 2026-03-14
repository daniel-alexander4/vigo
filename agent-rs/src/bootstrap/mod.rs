// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

pub mod client;
#[cfg(target_os = "linux")]
pub mod systemd;
#[cfg(target_os = "macos")]
pub mod launchd;
#[cfg(any(target_os = "freebsd", target_os = "netbsd"))]
pub mod rc_d;
#[cfg(target_os = "openbsd")]
pub mod rcctl;
#[cfg(target_os = "illumos")]
pub mod smf;
#[cfg(windows)]
pub mod windows_service;

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
///
/// "http://hobd.example.com:8181" + "1530" → "hobd.example.com:1530"
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    let mut host = bootstrap_server;
    host = host.strip_prefix("http://").unwrap_or(host);
    host = host.strip_prefix("https://").unwrap_or(host);
    // Strip port if present.
    if let Some(colon_pos) = host.rfind(':') {
        // Only strip if it looks like a port (all digits after colon).
        if host[colon_pos + 1..].chars().all(|c| c.is_ascii_digit()) {
            host = &host[..colon_pos];
        }
    }
    format!("{}:{}", host, grpc_port)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_derive_grpc_addr() {
        assert_eq!(
            derive_grpc_addr("http://hobd.example.com:8181", "1530"),
            "hobd.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("https://hobd.example.com:8181", "1530"),
            "hobd.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("hobd.example.com", "1530"),
            "hobd.example.com:1530"
        );
    }
}
