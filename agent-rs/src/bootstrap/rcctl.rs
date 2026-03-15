// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! rcctl service installation for the Hobnob agent on OpenBSD.

use anyhow::{bail, Context, Result};
use std::path::Path;
use tracing::info;

const INSTALL_PATH: &str = "/usr/local/sbin/hobnob";
const SERVICE_NAME: &str = "hobnob_thrall";
const RC_D_PATH: &str = "/etc/rc.d/hobnob_thrall";

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    super::derive_grpc_addr(bootstrap_server, grpc_port)
}

/// Generate the rc.d script content for OpenBSD.
fn rc_script() -> String {
    format!(
        r#"#!/bin/ksh

daemon="{bin}"
daemon_flags=""

. /etc/rc.d/rc.subr

rc_cmd $1
"#,
        bin = INSTALL_PATH,
    )
}

/// Install and start the rcctl service. Returns the gRPC address used.
pub async fn install_service(
    bootstrap_server: &str,
    grpc_port: &str,
    _data_dir: &Path,
) -> Result<String> {
    if !Path::new("/usr/sbin/rcctl").exists() {
        bail!("rcctl not found");
    }

    let grpc_addr = derive_grpc_addr(bootstrap_server, grpc_port);

    // Copy running binary.
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

    // Write rc.d script.
    let script = rc_script();
    std::fs::write(RC_D_PATH, &script).context("write rc.d script")?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(RC_D_PATH, std::fs::Permissions::from_mode(0o755))?;
    }

    // Enable and start via rcctl.
    for args in [
        vec!["rcctl", "enable", SERVICE_NAME],
        vec!["rcctl", "start", SERVICE_NAME],
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
    println!("  rcctl check {}    # check status", SERVICE_NAME);

    Ok(grpc_addr)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_rc_script() {
        let script = rc_script();
        assert!(script.contains("/usr/local/sbin/hobnob"));
        assert!(!script.contains("--server"), "service should read config from file");
        assert!(script.contains("rc.subr"));
    }
}
