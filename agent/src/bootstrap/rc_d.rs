// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! rc.d service installation for the Vigo agent on FreeBSD/NetBSD.

use anyhow::{bail, Context, Result};
use std::path::Path;
use tracing::info;

const INSTALL_PATH: &str = "/usr/local/sbin/vigo";
const SERVICE_NAME: &str = "vigo_envoy";

/// Path to the rc.d script.
fn rc_d_path() -> &'static str {
    if std::env::consts::OS == "netbsd" {
        "/etc/rc.d/vigo_envoy"
    } else {
        "/usr/local/etc/rc.d/vigo_envoy"
    }
}

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    super::derive_grpc_addr(bootstrap_server, grpc_port)
}

/// Generate the rc.d script content.
fn rc_script() -> String {
    format!(
        r#"#!/bin/sh
#
# PROVIDE: vigo_envoy
# REQUIRE: NETWORKING
# KEYWORD: shutdown

. /etc/rc.subr

name="{name}"
rcvar="{name}_enable"
command="{bin}"
command_args=""
pidfile="/var/run/${{name}}.pid"
command_interpreter=""

{name}_enable="${{{name}_enable:=NO}}"

run_rc_command "$1"
"#,
        name = SERVICE_NAME,
        bin = INSTALL_PATH,
    )
}

/// Install and start the rc.d service. Returns the gRPC address used.
pub async fn install_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
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

    // ADR-015: install the git-remote-gitback symlink. Best-effort.
    if let Err(e) = super::ensure_git_remote_gitback_symlink(INSTALL_PATH) {
        tracing::warn!(
            error = %e,
            link = super::GIT_REMOTE_GITBACK_SYMLINK,
            target = INSTALL_PATH,
            "could not install git-remote-gitback symlink; gitback:// git URLs unavailable until fixed"
        );
    }

    // Write rc.d script.
    let script = rc_script();
    let script_path = rc_d_path();
    std::fs::write(script_path, &script).context("write rc.d script")?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(script_path, std::fs::Permissions::from_mode(0o755))?;
    }

    // Enable service.
    if std::env::consts::OS == "freebsd" {
        let status = tokio::process::Command::new("sysrc")
            .args([&format!("{}_enable=YES", SERVICE_NAME)])
            .status()
            .await
            .context("sysrc enable")?;
        if !status.success() {
            bail!("sysrc exited with {}", status);
        }
    } else {
        // NetBSD: add to rc.conf manually
        let rc_conf = std::fs::read_to_string("/etc/rc.conf").unwrap_or_default();
        let line = format!("{}=YES", SERVICE_NAME);
        if !rc_conf.contains(&line) {
            std::fs::write("/etc/rc.conf", format!("{}\n{}\n", rc_conf.trim(), line))
                .context("write /etc/rc.conf")?;
        }
    }

    // Start service.
    let status = tokio::process::Command::new("service")
        .args([SERVICE_NAME, "start"])
        .status()
        .await
        .context("service start")?;
    if !status.success() {
        bail!("service {} start exited with {}", SERVICE_NAME, status);
    }

    println!("Vigo agent service installed and started.");
    println!("  service {} status    # check status", SERVICE_NAME);

    Ok(grpc_addr)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_rc_script() {
        let script = rc_script();
        assert!(script.contains("vigo_envoy"));
        assert!(script.contains("/usr/local/sbin/vigo"));
        assert!(!script.contains("--server"), "service should read config from file");
        assert!(script.contains("PROVIDE: vigo_envoy"));
    }
}
