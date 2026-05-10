// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

/// Standard install location for the `git-remote-swarmgit` multi-call
/// symlink (ADR-015). The symlink target is the agent binary
/// (`/usr/local/sbin/vigo`); git looks up `git-remote-<scheme>` on
/// PATH at push/pull time, so the symlink has to live on a directory
/// in user PATH (`/usr/local/bin/`) rather than the sbin dir where
/// the binary itself lives.
#[cfg(unix)]
pub const GIT_REMOTE_SWARMGIT_SYMLINK: &str = "/usr/local/bin/git-remote-swarmgit";

/// Pre-rename name of the helper symlink (`vigo://` scheme, before the
/// `swarmgit://` rename). `ensure_git_remote_swarmgit_symlink` removes
/// it on every install/update so no stale `git-remote-vigo` lingers on
/// PATH after an upgrade.
#[cfg(unix)]
const LEGACY_GIT_REMOTE_VIGO_SYMLINK: &str = "/usr/local/bin/git-remote-vigo";

/// Create or refresh the `git-remote-swarmgit` symlink so plain
/// `git push swarmgit://...` finds the agent binary's multi-call
/// dispatch entry point (ADR-015). Idempotent: if the link already
/// points at `target`, no-op; if it exists pointing somewhere else
/// (or as a regular file), it's removed and recreated. Also removes a
/// stale `git-remote-vigo` symlink left by pre-`swarmgit://` installs.
///
/// Best-effort — a symlink failure does not block agent install.
/// The agent functions normally without `git-remote-swarmgit` available;
/// the user just can't `git push swarmgit://` until the link is in place.
#[cfg(unix)]
pub fn ensure_git_remote_swarmgit_symlink(target: &str) -> std::io::Result<()> {
    use std::os::unix::fs::symlink;
    use std::path::PathBuf;
    // Drop the pre-rename helper if it's still around. Best-effort.
    let _ = std::fs::remove_file(LEGACY_GIT_REMOTE_VIGO_SYMLINK);
    let link = PathBuf::from(GIT_REMOTE_SWARMGIT_SYMLINK);
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
pub fn ensure_git_remote_swarmgit_symlink(_target: &str) -> std::io::Result<()> {
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
}
