// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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

/// Standard install location for the `git-remote-gitback` multi-call
/// symlink (ADR-015). The symlink target is the agent binary
/// (`/usr/local/sbin/vigo`); git looks up `git-remote-<scheme>` on
/// PATH at push/pull time, so the symlink has to live on a directory
/// in user PATH (`/usr/local/bin/`) rather than the sbin dir where
/// the binary itself lives.
#[cfg(unix)]
pub const GIT_REMOTE_GITBACK_SYMLINK: &str = "/usr/local/bin/git-remote-gitback";

/// Pre-rename name of the helper symlink (`vigo://` scheme, before the
/// `gitback://` rename). `ensure_git_remote_gitback_symlink` removes
/// it on every install/update so no stale `git-remote-vigo` lingers on
/// PATH after an upgrade.
#[cfg(unix)]
const LEGACY_GIT_REMOTE_VIGO_SYMLINK: &str = "/usr/local/bin/git-remote-vigo";

/// Create or refresh the `git-remote-gitback` symlink so plain
/// `git push gitback://...` finds the agent binary's multi-call
/// dispatch entry point (ADR-015). Idempotent: if the link already
/// points at `target`, no-op; if it exists pointing somewhere else
/// (or as a regular file), it's removed and recreated. Also removes a
/// stale `git-remote-vigo` symlink left by pre-`gitback://` installs.
///
/// Best-effort — a symlink failure does not block agent install.
/// The agent functions normally without `git-remote-gitback` available;
/// the user just can't `git push gitback://` until the link is in place.
#[cfg(unix)]
pub fn ensure_git_remote_gitback_symlink(target: &str) -> std::io::Result<()> {
    use std::os::unix::fs::symlink;
    use std::path::PathBuf;
    // Drop the pre-rename helper if it's still around. Best-effort.
    let _ = std::fs::remove_file(LEGACY_GIT_REMOTE_VIGO_SYMLINK);
    let link = PathBuf::from(GIT_REMOTE_GITBACK_SYMLINK);
    if let Some(parent) = link.parent() {
        std::fs::create_dir_all(parent)?;
    }
    match std::fs::read_link(&link) {
        Ok(existing) if existing == std::path::Path::new(target) => {
            return Ok(());
        }
        Ok(_) | Err(_) => {
            // Either points elsewhere, is a regular file, or doesn't
            // exist. Remove + recreate.
            let _ = std::fs::remove_file(&link);
        }
    }
    symlink(target, &link)
}

#[cfg(not(unix))]
pub fn ensure_git_remote_gitback_symlink(_target: &str) -> std::io::Result<()> {
    // Windows multi-call dispatch is deferred per ADR-015's
    // Linux-first doctrine. When the Windows port lands, this is
    // where the equivalent shim (a tiny `.exe` wrapper, since NTFS
    // junctions don't get inherited by argv[0] in a usefully
    // observable way) will install.
    Ok(())
}

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
///
/// "http://vigosrv.example.com:8443" + "1530" → "vigosrv.example.com:1530"
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    format!("{}:{}", host_only(bootstrap_server), grpc_port)
}

/// Host portion of a server address: scheme and port removed, an IPv6
/// literal's brackets RETAINED (`[2605::1]`), which is what a URL authority
/// and a gRPC endpoint both want.
///
/// Splitting on the first colon does not work here — that is what an IPv6
/// address is made of. Stripping the port by rfind + all-digits is safe
/// because a bracketed address always ends in `]`.
pub fn host_only(addr: &str) -> &str {
    let mut host = addr;
    host = host.strip_prefix("http://").unwrap_or(host);
    host = host.strip_prefix("https://").unwrap_or(host);
    // Strip port if present.
    if let Some(colon_pos) = host.rfind(':') {
        // Only strip if it looks like a port (all digits after colon).
        if host[colon_pos + 1..].chars().all(|c| c.is_ascii_digit()) {
            host = &host[..colon_pos];
        }
    }
    host
}

/// Host as rustls wants it for the TLS server name: an IPv6 literal with its
/// brackets REMOVED, so `ServerName::try_from` parses it as an IP address and
/// checks it against the certificate's IP SAN. Everything else is unchanged.
///
/// Passing the bracketed form here fails the handshake outright — tonic
/// rejected `domain_name("[2605")` with a bare "configure TLS" error, which is
/// how this surfaced.
pub fn tls_server_name(addr: &str) -> &str {
    let host = host_only(addr);
    host.strip_prefix('[')
        .and_then(|h| h.strip_suffix(']'))
        .unwrap_or(host)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_derive_grpc_addr() {
        assert_eq!(
            derive_grpc_addr("http://vigosrv.example.com:8443", "1530"),
            "vigosrv.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("https://vigosrv.example.com:8443", "1530"),
            "vigosrv.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("vigosrv.example.com", "1530"),
            "vigosrv.example.com:1530"
        );
    }

    /// An IPv6 authority is delimited by brackets, not by a colon. The port
    /// strip here is rfind + all-digits, which leaves the brackets intact —
    /// the property main.rs's old inline `.split(':').next()` lacked, where
    /// https://[2605:59ca::1]:8443 collapsed to "[2605" and the agent
    /// persisted `server = [2605:1530` and never checked in again.
    #[test]
    fn tls_server_name_strips_ipv6_brackets() {
        assert_eq!(
            tls_server_name("[2605:59ca:351a:aa10:ca2d:4a18:1168:6a4c]:1530"),
            "2605:59ca:351a:aa10:ca2d:4a18:1168:6a4c"
        );
        assert_eq!(tls_server_name("https://[::1]:8443"), "::1");
        // v4 and DNS are untouched.
        assert_eq!(tls_server_name("https://192.168.1.215:8443"), "192.168.1.215");
        assert_eq!(tls_server_name("danlap:1530"), "danlap");
    }

    #[test]
    fn derive_grpc_addr_keeps_ipv6_brackets() {
        assert_eq!(
            derive_grpc_addr("https://[2605:59ca:351a:aa10:ca2d:4a18:1168:6a4c]:8443", "1530"),
            "[2605:59ca:351a:aa10:ca2d:4a18:1168:6a4c]:1530"
        );
        assert_eq!(derive_grpc_addr("https://[::1]:8443", "1530"), "[::1]:1530");
        // No port to strip.
        assert_eq!(derive_grpc_addr("https://[::1]", "1530"), "[::1]:1530");
        // A host whose final group is all digits must not be mistaken for a port.
        assert_eq!(derive_grpc_addr("https://[fd48:4af5:19b5:10::8443]", "1530"),
                   "[fd48:4af5:19b5:10::8443]:1530");
    }
}
