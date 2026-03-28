use anyhow::{Context, Result};
use std::process::Command;

const SERVICE_UNIT: &str = r#"[Unit]
Description=Connwaer Compliance Verification Agent
After=network.target vigo-envoy.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/sbin/connwaer run
Restart=always
RestartSec=30
LimitNOFILE=65536
StandardOutput=journal
StandardError=journal
SyslogIdentifier=connwaer

[Install]
WantedBy=multi-user.target
"#;

const UNIT_PATH: &str = "/etc/systemd/system/connwaer.service";

pub fn install() -> Result<()> {
    // Copy binary to install location.
    let current_exe = std::env::current_exe()
        .context("getting current executable path")?;
    let install_path = crate::platform::paths::DEFAULT_BINARY_PATH;

    if current_exe.to_string_lossy() != install_path {
        std::fs::copy(&current_exe, install_path)
            .with_context(|| format!("copying binary to {install_path}"))?;

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(install_path, std::fs::Permissions::from_mode(0o755))?;
        }
        println!("installed binary to {install_path}");
    }

    // Create config directory.
    let config_dir = std::path::Path::new(crate::platform::paths::DEFAULT_CONFIG_PATH)
        .parent()
        .unwrap_or(std::path::Path::new("/etc/connwaer"));
    std::fs::create_dir_all(config_dir)
        .with_context(|| format!("creating {}", config_dir.display()))?;

    // Create data directories.
    let data_dir = crate::platform::paths::DEFAULT_DATA_DIR;
    std::fs::create_dir_all(data_dir)
        .with_context(|| format!("creating {data_dir}"))?;
    let results_dir = crate::platform::paths::DEFAULT_RESULTS_DIR;
    std::fs::create_dir_all(results_dir)
        .with_context(|| format!("creating {results_dir}"))?;

    // Write service unit.
    std::fs::write(UNIT_PATH, SERVICE_UNIT)
        .with_context(|| format!("writing {UNIT_PATH}"))?;
    println!("wrote {UNIT_PATH}");

    // Reload systemd.
    let status = Command::new("systemctl")
        .args(["daemon-reload"])
        .status()
        .context("systemctl daemon-reload")?;
    if !status.success() {
        anyhow::bail!("systemctl daemon-reload failed");
    }

    // Enable.
    let status = Command::new("systemctl")
        .args(["enable", "connwaer"])
        .status()
        .context("systemctl enable")?;
    if !status.success() {
        anyhow::bail!("systemctl enable connwaer failed");
    }
    println!("enabled connwaer service");

    // Start.
    let status = Command::new("systemctl")
        .args(["start", "connwaer"])
        .status()
        .context("systemctl start")?;
    if !status.success() {
        anyhow::bail!("systemctl start connwaer failed");
    }
    println!("started connwaer service");

    Ok(())
}
