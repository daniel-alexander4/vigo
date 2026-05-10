// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Launchd service installation for the Vigo agent on macOS.

use anyhow::{bail, Context, Result};
use std::path::Path;
use tracing::info;

const INSTALL_PATH: &str = "/usr/local/sbin/vigo";
const SERVICE_LABEL: &str = "com.vigo.envoy";
const PLIST_PATH: &str = "/Library/LaunchDaemons/com.vigo.envoy.plist";

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    super::derive_grpc_addr(bootstrap_server, grpc_port)
}

/// Generate the launchd plist content.
/// The agent reads server address and data directory from the config file
/// written during bootstrap (and the hard-coded platform defaults).
fn plist_content() -> String {
    format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/var/log/vigo-envoy.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/vigo-envoy.log</string>
</dict>
</plist>
"#,
        SERVICE_LABEL, INSTALL_PATH
    )
}

/// Install and start the launchd service. Returns the gRPC address used.
pub async fn install_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
    if !Path::new("/bin/launchctl").exists() {
        bail!("launchctl not found");
    }

    let grpc_addr = derive_grpc_addr(bootstrap_server, grpc_port);

    // Copy running binary to install path.
    let src_path = std::env::current_exe().context("resolve executable path")?;
    let src_path = std::fs::canonicalize(&src_path).context("resolve symlinks")?;
    if src_path.to_str() != Some(INSTALL_PATH) {
        std::fs::copy(&src_path, INSTALL_PATH)
            .with_context(|| format!("install binary to {}", INSTALL_PATH))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(INSTALL_PATH, std::fs::Permissions::from_mode(0o755))?;
        }
        info!(path = INSTALL_PATH, "installed vigo binary");
    }

    // ADR-015: install the git-remote-swarmgit symlink. Best-effort.
    if let Err(e) = super::ensure_git_remote_swarmgit_symlink(INSTALL_PATH) {
        tracing::warn!(
            error = %e,
            link = super::GIT_REMOTE_SWARMGIT_SYMLINK,
            target = INSTALL_PATH,
            "could not install git-remote-swarmgit symlink; swarmgit:// git URLs unavailable until fixed"
        );
    }

    // Write plist.
    let plist = plist_content();
    std::fs::write(PLIST_PATH, &plist).context("write launchd plist")?;

    // Load and start.
    let status = tokio::process::Command::new("launchctl")
        .args(["load", "-w", PLIST_PATH])
        .status()
        .await
        .context("launchctl load")?;
    if !status.success() {
        bail!("launchctl load exited with {}", status);
    }

    println!("Vigo agent service installed and started.");
    println!("  launchctl list {}    # check status", SERVICE_LABEL);
    println!("  tail -f /var/log/vigo-envoy.log    # follow logs");

    Ok(grpc_addr)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_plist_content() {
        let plist = plist_content();
        assert!(plist.contains("com.vigo.envoy"));
        assert!(plist.contains("/usr/local/sbin/vigo"));
        assert!(!plist.contains("--server"), "service should read config from file");
        assert!(!plist.contains("--data-dir"), "service uses default_data_dir()");
        assert!(plist.contains("KeepAlive"));
    }
}
