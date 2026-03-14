// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Launchd service installation for the Hobnob agent on macOS.

use anyhow::{bail, Context, Result};
use std::path::Path;
use tracing::info;

const INSTALL_PATH: &str = "/usr/local/bin/hobnob";
const SERVICE_LABEL: &str = "com.hobnob.thrall";
const PLIST_PATH: &str = "/Library/LaunchDaemons/com.hobnob.thrall.plist";

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    super::derive_grpc_addr(bootstrap_server, grpc_port)
}

/// Generate the launchd plist content.
fn plist_content(grpc_addr: &str, data_dir: &str) -> String {
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
        <string>--server</string>
        <string>{}</string>
        <string>--data-dir</string>
        <string>{}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/var/log/hobnob-thrall.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/hobnob-thrall.log</string>
</dict>
</plist>
"#,
        SERVICE_LABEL, INSTALL_PATH, grpc_addr, data_dir
    )
}

/// Install and start the launchd service. Returns the gRPC address used.
pub async fn install_service(
    bootstrap_server: &str,
    grpc_port: &str,
    data_dir: &Path,
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
        info!(path = INSTALL_PATH, "installed hobnob binary");
    }

    // Write plist.
    let data_dir_str = data_dir.to_string_lossy();
    let plist = plist_content(&grpc_addr, &data_dir_str);
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

    println!("Hobnob agent service installed and started.");
    println!("  launchctl list {}    # check status", SERVICE_LABEL);
    println!("  tail -f /var/log/hobnob-thrall.log    # follow logs");

    Ok(grpc_addr)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_plist_content() {
        let plist = plist_content("hobd.example.com:1530", "/usr/local/etc/hobnob-thrall");
        assert!(plist.contains("com.hobnob.thrall"));
        assert!(plist.contains("/usr/local/bin/hobnob"));
        assert!(plist.contains("hobd.example.com:1530"));
        assert!(plist.contains("KeepAlive"));
    }
}
