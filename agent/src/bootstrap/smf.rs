// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! SMF service installation for the Vigo agent on illumos (OmniOS, OpenIndiana, SmartOS).

use anyhow::{bail, Context, Result};
use std::path::Path;
use tracing::info;

const INSTALL_PATH: &str = "/usr/local/sbin/vigo";
const MANIFEST_PATH: &str = "/var/svc/manifest/system/vigo-envoy.xml";
const FMRI: &str = "svc:/system/vigo-envoy:default";

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    super::derive_grpc_addr(bootstrap_server, grpc_port)
}

/// Generate the SMF manifest XML content.
fn smf_manifest() -> String {
    format!(
        r#"<?xml version="1.0"?>
<!DOCTYPE service_bundle SYSTEM "/usr/share/lib/xml/dtd/service_bundle.dtd.1">
<service_bundle type="manifest" name="vigo-envoy">
  <service name="system/vigo-envoy" type="service" version="1">
    <create_default_instance enabled="false"/>
    <single_instance/>

    <dependency name="network" grouping="require_all" restart_on="error" type="service">
      <service_fmri value="svc:/milestone/network:default"/>
    </dependency>

    <dependency name="filesystem" grouping="require_all" restart_on="error" type="service">
      <service_fmri value="svc:/system/filesystem/local"/>
    </dependency>

    <exec_method type="method" name="start"
      exec="{bin} &amp;"
      timeout_seconds="60">
      <method_context>
        <method_credential user="root" group="root"/>
      </method_context>
    </exec_method>

    <exec_method type="method" name="stop" exec=":kill" timeout_seconds="60"/>

    <stability value="Evolving"/>

    <template>
      <common_name>
        <loctext xml:lang="C">Vigo Configuration Management Agent</loctext>
      </common_name>
    </template>
  </service>
</service_bundle>
"#,
        bin = INSTALL_PATH,
    )
}

/// Install and start the SMF service. Returns the gRPC address used.
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

    // ADR-015: install the git-remote-swarmgit symlink. Best-effort.
    if let Err(e) = super::ensure_git_remote_swarmgit_symlink(INSTALL_PATH) {
        tracing::warn!(
            error = %e,
            link = super::GIT_REMOTE_SWARMGIT_SYMLINK,
            target = INSTALL_PATH,
            "could not install git-remote-swarmgit symlink; swarmgit:// git URLs unavailable until fixed"
        );
    }

    // Write SMF manifest.
    let manifest = smf_manifest();
    std::fs::write(MANIFEST_PATH, &manifest).context("write SMF manifest")?;

    // Import and enable the service.
    let status = tokio::process::Command::new("svccfg")
        .args(["import", MANIFEST_PATH])
        .status()
        .await
        .context("svccfg import")?;
    if !status.success() {
        bail!("svccfg import exited with {}", status);
    }

    let status = tokio::process::Command::new("svcadm")
        .args(["enable", FMRI])
        .status()
        .await
        .context("svcadm enable")?;
    if !status.success() {
        bail!("svcadm enable exited with {}", status);
    }

    println!("Vigo agent service installed and started (SMF).");
    println!("  svcs vigo-envoy    # check status");

    Ok(grpc_addr)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_smf_manifest() {
        let manifest = smf_manifest();
        assert!(manifest.contains("vigo-envoy"));
        assert!(manifest.contains("/usr/local/sbin/vigo"));
        assert!(!manifest.contains("--server"), "service should read config from file");
        assert!(manifest.contains("service_bundle"));
        assert!(manifest.contains("system/vigo-envoy"));
        assert!(manifest.contains("milestone/network"));
    }
}
