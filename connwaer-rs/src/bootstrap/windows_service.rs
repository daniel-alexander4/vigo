use anyhow::{Context, Result};
use std::process::Command;

pub fn install() -> Result<()> {
    let current_exe = std::env::current_exe()
        .context("getting current executable path")?;
    let install_path = crate::platform::paths::DEFAULT_BINARY_PATH;

    // Create directories.
    let install_dir = std::path::Path::new(install_path).parent()
        .unwrap_or(std::path::Path::new(r"C:\Program Files\Connwaer"));
    std::fs::create_dir_all(install_dir)?;

    let data_dir = crate::platform::paths::DEFAULT_DATA_DIR;
    std::fs::create_dir_all(data_dir)?;
    let results_dir = crate::platform::paths::DEFAULT_RESULTS_DIR;
    std::fs::create_dir_all(results_dir)?;

    // Copy binary.
    if current_exe.to_string_lossy() != install_path {
        std::fs::copy(&current_exe, install_path)
            .with_context(|| format!("copying binary to {install_path}"))?;
        println!("installed binary to {install_path}");
    }

    // Create Windows service via sc.exe.
    let status = Command::new("sc.exe")
        .args([
            "create", "Connwaer",
            &format!("binPath={install_path} run"),
            "start=auto",
            "DisplayName=Connwaer Compliance Verification Agent",
        ])
        .status()
        .context("sc.exe create")?;

    if !status.success() {
        anyhow::bail!("sc.exe create Connwaer failed");
    }
    println!("created Connwaer service");

    // Configure restart on failure.
    let _ = Command::new("sc.exe")
        .args(["failure", "Connwaer", "reset=86400", "actions=restart/30000/restart/60000/restart/120000"])
        .status();

    // Start.
    let status = Command::new("sc.exe")
        .args(["start", "Connwaer"])
        .status()
        .context("sc.exe start")?;

    if !status.success() {
        anyhow::bail!("sc.exe start Connwaer failed");
    }
    println!("started Connwaer service");

    Ok(())
}
