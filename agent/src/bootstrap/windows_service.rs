// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Windows Service installation for the Vigo agent.

#![cfg(windows)]

use anyhow::{bail, Context, Result};
use std::path::Path;
use tracing::info;

use crate::platform::paths;

const SERVICE_NAME: &str = "vigo-envoy";
const SERVICE_DISPLAY: &str = "Vigo Configuration Management Agent";

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
///
/// "http://vigosrv.example.com:8443" + "1530" → "vigosrv.example.com:1530"
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    let mut host = bootstrap_server;
    host = host.strip_prefix("http://").unwrap_or(host);
    host = host.strip_prefix("https://").unwrap_or(host);
    if let Some(colon_pos) = host.rfind(':') {
        if host[colon_pos + 1..].chars().all(|c| c.is_ascii_digit()) {
            host = &host[..colon_pos];
        }
    }
    format!("{}:{}", host, grpc_port)
}

/// Install and start the Windows service. Returns the gRPC address used.
pub async fn install_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
    let grpc_addr = derive_grpc_addr(bootstrap_server, grpc_port);
    let binary_path = paths::default_binary_path();
    let binary_dir = binary_path.parent().unwrap_or(Path::new(r"C:\Program Files\Vigo"));

    // Create install directory.
    std::fs::create_dir_all(binary_dir)
        .with_context(|| format!("create install directory {}", binary_dir.display()))?;

    // Copy running binary to install location.
    let src_path = std::env::current_exe().context("resolve executable path")?;
    let src_path = std::fs::canonicalize(&src_path).context("resolve symlinks")?;
    if src_path != binary_path {
        std::fs::copy(&src_path, &binary_path)
            .with_context(|| format!("install binary to {}", binary_path.display()))?;
        info!(path = %binary_path.display(), "installed vigo binary");
    }

    let bin_path_str = binary_path.to_string_lossy();

    // Install service via sc.exe.
    let create_status = tokio::process::Command::new("sc.exe")
        .args([
            "create",
            SERVICE_NAME,
            &format!("binPath= \"{}\"", bin_path_str),
            "start=", "auto",
        ])
        .status()
        .await
        .context("run sc.exe create")?;

    if !create_status.success() {
        bail!("sc.exe create exited with {}", create_status);
    }

    // Set description.
    let _ = tokio::process::Command::new("sc.exe")
        .args(["description", SERVICE_NAME, SERVICE_DISPLAY])
        .status()
        .await;

    // Configure failure recovery: restart after 60s on each failure, reset counter after 24h.
    let _ = tokio::process::Command::new("sc.exe")
        .args([
            "failure",
            SERVICE_NAME,
            "reset=", "86400",
            "actions=", "restart/60000/restart/60000/restart/60000",
        ])
        .status()
        .await;

    // Start the service.
    let start_status = tokio::process::Command::new("sc.exe")
        .args(["start", SERVICE_NAME])
        .status()
        .await
        .context("run sc.exe start")?;

    if !start_status.success() {
        bail!("sc.exe start exited with {}", start_status);
    }

    println!("Vigo agent service installed and started.");
    println!("  sc.exe query {}    # check status", SERVICE_NAME);
    println!("  Get-EventLog -LogName Application -Source {} -Newest 20    # view logs", SERVICE_NAME);

    Ok(grpc_addr)
}
