// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Systemd service installation for the Vigo agent.

use anyhow::{bail, Context, Result};
use std::path::Path;
use tracing::info;

const INSTALL_PATH: &str = "/usr/local/sbin/vigo";
/// Legacy argv[0] shim symlink from 0.22.0-0.22.1. Removed on install/upgrade
/// so envoys that carried it get cleaned up. The `repo` subcommand is now
/// accessed exclusively as `vigo cairn <verb>`.
const LEGACY_CAIRN_SHIM_PATH: &str = "/usr/local/bin/cairn";
const SERVICE_NAME: &str = "vigo-envoy";
const UNIT_PATH: &str = "/etc/systemd/system/vigo-envoy.service";

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    super::derive_grpc_addr(bootstrap_server, grpc_port)
}

/// Generate the systemd unit file content.
/// The agent reads server address from the config file written during bootstrap.
fn unit_file() -> String {
    format!(
        "[Unit]\n\
         Description=Vigo Configuration Management Agent\n\
         After=network.target\n\
         Wants=network-online.target\n\
         \n\
         [Service]\n\
         Type=simple\n\
         ExecStart={}\n\
         Restart=always\n\
         RestartSec=10\n\
         LimitNOFILE=65536\n\
         \n\
         [Install]\n\
         WantedBy=multi-user.target\n",
        INSTALL_PATH
    )
}

/// Install and start the systemd service. Returns the gRPC address used.
pub async fn install_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
    // Check systemctl exists.
    if !Path::new("/usr/bin/systemctl").exists() && !Path::new("/bin/systemctl").exists() {
        bail!("systemctl not found");
    }

    let grpc_addr = derive_grpc_addr(bootstrap_server, grpc_port);

    // Copy running binary to /usr/local/sbin/vigo.
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

    // Remove the legacy /usr/local/bin/cairn shim symlink if
    // present (shipped in 0.22.0-0.22.1). The `repo` subcommand is
    // now accessed exclusively as `vigo cairn <verb>`; the symlink
    // would point at a binary that no longer honors argv[0]
    // detection and would confuse users into thinking it still
    // works.
    remove_legacy_cairn_shim();

    // Write unit file.
    let unit = unit_file();
    std::fs::write(UNIT_PATH, &unit).context("write systemd unit file")?;

    // daemon-reload, enable, start.
    for args in [
        vec!["systemctl", "daemon-reload"],
        vec!["systemctl", "enable", SERVICE_NAME],
        vec!["systemctl", "start", SERVICE_NAME],
    ] {
        let status = tokio::process::Command::new(args[0])
            .args(&args[1..])
            .status()
            .await
            .with_context(|| format!("run {}", args.join(" ")))?;
        if !status.success() {
            bail!("{} exited with {}", args.join(" "), status);
        }
    }

    println!("Vigo agent service installed and started.");
    println!("  systemctl status {}    # check status", SERVICE_NAME);
    println!("  journalctl -u {} -f    # follow logs", SERVICE_NAME);

    Ok(grpc_addr)
}

/// Remove the legacy /usr/local/bin/cairn shim symlink from 0.22.0-0.22.1.
/// Best-effort: if the path doesn't exist, does nothing. If the path exists
/// but isn't a symlink (operator replaced it with a real file), leave it
/// alone — we don't know what it is.
pub(crate) fn remove_legacy_cairn_shim() {
    let shim = Path::new(LEGACY_CAIRN_SHIM_PATH);
    let Ok(meta) = std::fs::symlink_metadata(shim) else {
        return;
    };
    if !meta.file_type().is_symlink() {
        return;
    }
    match std::fs::remove_file(shim) {
        Ok(()) => info!(path = LEGACY_CAIRN_SHIM_PATH, "removed legacy cairn shim symlink"),
        Err(e) => tracing::warn!(
            path = LEGACY_CAIRN_SHIM_PATH,
            error = %e,
            "failed to remove legacy cairn shim"
        ),
    }
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
            derive_grpc_addr("http://vigosrv.example.com", "1530"),
            "vigosrv.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("vigosrv.example.com:8443", "1530"),
            "vigosrv.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("http://10.0.0.1:8443", "1530"),
            "10.0.0.1:1530"
        );
    }

    #[test]
    fn test_unit_file_content() {
        let unit = unit_file();
        assert!(unit.contains("ExecStart=/usr/local/sbin/vigo"));
        assert!(!unit.contains("--server"), "service should read config from file, not CLI flags");
        assert!(unit.contains("Restart=always"));
        assert!(unit.contains("WantedBy=multi-user.target"));
    }
}
