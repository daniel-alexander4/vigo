// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Systemd service installation for the Hobnob agent.

use anyhow::{bail, Context, Result};
use std::path::Path;
use tracing::info;

const INSTALL_PATH: &str = "/usr/local/sbin/hobnob";
const SERVICE_NAME: &str = "hobnob-thrall";
const UNIT_PATH: &str = "/etc/systemd/system/hobnob-thrall.service";

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    super::derive_grpc_addr(bootstrap_server, grpc_port)
}

/// Generate the systemd unit file content.
/// The agent reads server address from the config file written during bootstrap.
fn unit_file() -> String {
    format!(
        "[Unit]\n\
         Description=Hobnob Configuration Management Agent\n\
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
    _data_dir: &Path,
) -> Result<String> {
    // Check systemctl exists.
    if !Path::new("/usr/bin/systemctl").exists() && !Path::new("/bin/systemctl").exists() {
        bail!("systemctl not found");
    }

    let grpc_addr = derive_grpc_addr(bootstrap_server, grpc_port);

    // Copy running binary to /usr/local/sbin/hobnob.
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

    println!("Hobnob agent service installed and started.");
    println!("  systemctl status {}    # check status", SERVICE_NAME);
    println!("  journalctl -u {} -f    # follow logs", SERVICE_NAME);

    Ok(grpc_addr)
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
            derive_grpc_addr("http://hobd.example.com", "1530"),
            "hobd.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("hobd.example.com:8181", "1530"),
            "hobd.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("http://10.0.0.1:8181", "1530"),
            "10.0.0.1:1530"
        );
    }

    #[test]
    fn test_unit_file_content() {
        let unit = unit_file();
        assert!(unit.contains("ExecStart=/usr/local/sbin/hobnob"));
        assert!(!unit.contains("--server"), "service should read config from file, not CLI flags");
        assert!(unit.contains("Restart=always"));
        assert!(unit.contains("WantedBy=multi-user.target"));
    }
}
