// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Agent self-update mechanism: version checking against the server, binary
//! download with SHA256 verification, atomic replacement, and process restart.
//!
//! Mirrors the Go `update` package.

use anyhow::{bail, Context, Result};
use sha2::{Digest, Sha256};
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use tracing::{debug, info, warn};

/// Server response describing the latest agent version.
#[derive(Debug, serde::Deserialize)]
pub struct VersionInfo {
    pub version: String,
    pub sha256: String,
    pub download_url: String,
}

/// Check the server's `/api/v1/agent/latest` endpoint for a newer version.
///
/// Returns `None` if the current version matches, the server reports no
/// binaries available, or the version field is empty.
pub async fn check_for_update(
    api_addr: &str,
    current_version: &str,
    os: &str,
    arch: &str,
) -> Result<Option<VersionInfo>> {
    let url = format!("{}/api/v1/agent/latest?os={}&arch={}", api_addr, os, arch);

    let resp = reqwest::Client::new()
        .get(&url)
        .send()
        .await
        .context("version check request")?;

    if !resp.status().is_success() {
        bail!("version check returned status {}", resp.status());
    }

    let mut info: VersionInfo = resp.json().await.context("decode version response")?;

    // No update if version is empty (no binaries available) or matches current.
    if info.version.is_empty() || info.version == current_version {
        return Ok(None);
    }

    // Build full download URL if relative.
    if info.download_url.starts_with('/') {
        info.download_url = format!("{}{}", api_addr, info.download_url);
    }

    Ok(Some(info))
}

/// Download the new agent binary, verify its SHA256 checksum, and atomically
/// replace the current binary.
///
/// On any error, the original binary is left untouched and temp files are
/// cleaned up.
///
/// If `exec_self` is true, calls `exec` to restart the process after
/// replacing the binary. In tests, pass `false` to skip the exec.
pub async fn apply(
    info: &VersionInfo,
    current_binary: &str,
    exec_self: bool,
) -> Result<()> {
    let dir = Path::new(current_binary)
        .parent()
        .unwrap_or(Path::new("/tmp"));

    // Download to temp file in same directory for atomic rename.
    let tmp_path = dir.join(format!(".hobnob-update-{}", std::process::id()));
    let _cleanup = TempFileGuard(tmp_path.clone());

    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(60))
        .build()
        .context("build HTTP client")?;

    let resp = client
        .get(&info.download_url)
        .send()
        .await
        .context("download binary")?;

    if !resp.status().is_success() {
        bail!("download returned status {}", resp.status());
    }

    let body = resp.bytes().await.context("download binary body")?;

    // Compute SHA256.
    let mut hasher = Sha256::new();
    hasher.update(&body);
    let computed = format!("{:x}", hasher.finalize());

    if computed != info.sha256 {
        bail!(
            "checksum mismatch: expected {}, got {}",
            info.sha256,
            computed
        );
    }

    // Write temp file.
    std::fs::write(&tmp_path, &body).context("write temp binary")?;
    std::fs::set_permissions(&tmp_path, std::fs::Permissions::from_mode(0o755))
        .context("chmod temp binary")?;

    // Atomic rename.
    std::fs::rename(&tmp_path, current_binary).context("atomic rename")?;

    info!(
        version = %info.version,
        sha256 = %info.sha256,
        "binary updated successfully"
    );

    // Exec replaces the current process.
    if exec_self {
        let args: Vec<std::ffi::CString> = std::env::args()
            .map(|a| std::ffi::CString::new(a).unwrap())
            .collect();
        let binary = std::ffi::CString::new(current_binary).unwrap();
        nix::unistd::execv(&binary, &args).context("exec new binary")?;
    }

    Ok(())
}

/// Check for a self-update and apply it if available.
///
/// Failures are logged but never crash the agent.
pub async fn check_self_update(api_addr: &str, current_version: &str) {
    let os = std::env::consts::OS;
    let arch = std::env::consts::ARCH;

    match check_for_update(api_addr, current_version, os, arch).await {
        Ok(Some(info)) => {
            info!(
                current = current_version,
                new = %info.version,
                "newer agent version available, updating"
            );

            let current_binary = match std::env::current_exe() {
                Ok(p) => p.to_string_lossy().to_string(),
                Err(e) => {
                    warn!(error = %e, "cannot determine current binary path");
                    return;
                }
            };

            if let Err(e) = apply(&info, &current_binary, true).await {
                warn!(error = %e, "self-update apply failed, continuing with current version");
            }
        }
        Ok(None) => {
            debug!(version = current_version, "agent is up to date");
        }
        Err(e) => {
            warn!(error = %e, "self-update version check failed");
        }
    }
}

/// RAII guard that removes a temp file on drop (if it still exists).
struct TempFileGuard(std::path::PathBuf);

impl Drop for TempFileGuard {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.0);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn check_for_update_returns_info_when_newer() {
        let mut server = mockito::Server::new_async().await;
        let _m = server
            .mock("GET", "/api/v1/agent/latest?os=linux&arch=x86_64")
            .with_body(
                r#"{"version":"2.0.0","sha256":"abc123","download_url":"/api/v1/agent/download/linux/amd64"}"#,
            )
            .create_async()
            .await;

        let info = check_for_update(&server.url(), "1.0.0", "linux", "x86_64")
            .await
            .unwrap();
        let info = info.unwrap();
        assert_eq!(info.version, "2.0.0");
        assert_eq!(info.sha256, "abc123");
        assert!(info.download_url.ends_with("/api/v1/agent/download/linux/amd64"));
    }

    #[tokio::test]
    async fn check_for_update_returns_none_when_version_matches() {
        let mut server = mockito::Server::new_async().await;
        let _m = server
            .mock("GET", "/api/v1/agent/latest?os=linux&arch=x86_64")
            .with_body(r#"{"version":"1.0.0","sha256":"abc","download_url":"/dl"}"#)
            .create_async()
            .await;

        let info = check_for_update(&server.url(), "1.0.0", "linux", "x86_64")
            .await
            .unwrap();
        assert!(info.is_none());
    }

    #[tokio::test]
    async fn check_for_update_returns_none_when_empty_version() {
        let mut server = mockito::Server::new_async().await;
        let _m = server
            .mock("GET", "/api/v1/agent/latest?os=linux&arch=x86_64")
            .with_body(r#"{"version":"","sha256":"","download_url":""}"#)
            .create_async()
            .await;

        let info = check_for_update(&server.url(), "1.0.0", "linux", "x86_64")
            .await
            .unwrap();
        assert!(info.is_none());
    }

    #[tokio::test]
    async fn apply_downloads_verifies_and_replaces() {
        let binary_content = b"new-agent-binary-v2";
        let checksum = format!("{:x}", Sha256::digest(binary_content));

        let mut server = mockito::Server::new_async().await;
        let _m = server
            .mock("GET", "/download")
            .with_body(binary_content.as_slice())
            .create_async()
            .await;

        let dir = tempfile::tempdir().unwrap();
        let current = dir.path().join("hobnob");
        std::fs::write(&current, b"old-binary-v1").unwrap();

        let info = VersionInfo {
            version: "2.0.0".into(),
            sha256: checksum,
            download_url: format!("{}/download", server.url()),
        };

        apply(&info, current.to_str().unwrap(), false)
            .await
            .unwrap();

        let content = std::fs::read(&current).unwrap();
        assert_eq!(content, binary_content);
    }

    #[tokio::test]
    async fn apply_rejects_checksum_mismatch() {
        let mut server = mockito::Server::new_async().await;
        let _m = server
            .mock("GET", "/download")
            .with_body(b"some-binary".as_slice())
            .create_async()
            .await;

        let dir = tempfile::tempdir().unwrap();
        let current = dir.path().join("hobnob");
        std::fs::write(&current, b"old").unwrap();

        let info = VersionInfo {
            version: "2.0.0".into(),
            sha256: "wrong-checksum".into(),
            download_url: format!("{}/download", server.url()),
        };

        let err = apply(&info, current.to_str().unwrap(), false).await;
        assert!(err.is_err());
        assert!(err.unwrap_err().to_string().contains("checksum mismatch"));

        // Original binary untouched.
        assert_eq!(std::fs::read(&current).unwrap(), b"old");
    }

    #[tokio::test]
    async fn apply_cleans_up_temp_on_error() {
        let mut server = mockito::Server::new_async().await;
        let _m = server
            .mock("GET", "/download")
            .with_body(b"binary-data".as_slice())
            .create_async()
            .await;

        let dir = tempfile::tempdir().unwrap();
        let current = dir.path().join("hobnob");
        std::fs::write(&current, b"old").unwrap();

        let info = VersionInfo {
            version: "2.0.0".into(),
            sha256: "bad".into(),
            download_url: format!("{}/download", server.url()),
        };

        let _ = apply(&info, current.to_str().unwrap(), false).await;

        // No temp files should remain.
        let entries: Vec<_> = std::fs::read_dir(dir.path())
            .unwrap()
            .filter_map(|e| e.ok())
            .filter(|e| e.file_name() != "hobnob")
            .collect();
        assert!(entries.is_empty(), "leftover temp files: {:?}", entries);
    }
}
