// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Agent self-update mechanism: version checking against the server, binary
//! download with SHA256 + ED25519 signature verification, atomic replacement,
//! and process restart.

use anyhow::{bail, Context, Result};
use ed25519_dalek::{Signature, VerifyingKey};
use sha2::{Digest, Sha256};
use std::path::Path;
use tracing::{debug, info, warn};

/// Server response describing the latest agent version.
#[derive(Debug, serde::Deserialize)]
pub struct VersionInfo {
    pub version: String,
    pub sha256: String,
    #[serde(default)]
    pub signature: String, // hex-encoded ED25519 signature over SHA256 digest
    pub download_url: String,
}

/// TLS configuration for mTLS downloads.
#[derive(Clone)]
pub struct TlsCerts {
    pub ca_cert: String,     // path to CA cert PEM
    pub client_cert: String, // path to client cert PEM
    pub client_key: String,  // path to client key PEM
}

/// Build a ureq agent with mTLS if certs are available, otherwise plain HTTPS.
fn build_agent(tls: Option<&TlsCerts>) -> ureq::Agent {
    if let Some(certs) = tls {
        if let Ok(agent) = build_mtls_agent(certs) {
            return agent;
        }
        warn!("failed to configure mTLS for update download, falling back to plain HTTPS");
    }
    ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .timeout_global(Some(std::time::Duration::from_secs(60)))
            .build(),
    )
}

fn build_mtls_agent(certs: &TlsCerts) -> Result<ureq::Agent> {
    use ureq::tls::{parse_pem, PemItem};

    let ca_pem = std::fs::read(&certs.ca_cert).context("read CA cert")?;
    let cert_pem = std::fs::read(&certs.client_cert).context("read client cert")?;
    let key_pem = std::fs::read(&certs.client_key).context("read client key")?;

    // Parse CA certificate.
    let mut ca_certs = Vec::new();
    for item in parse_pem(&ca_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            ca_certs.push(c);
        }
    }
    let root_certs = ureq::tls::RootCerts::new_with_certs(&ca_certs);

    // Parse client cert chain.
    let mut client_certs = Vec::new();
    for item in parse_pem(&cert_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            client_certs.push(c);
        }
    }

    // Parse client private key.
    let mut client_key = None;
    for item in parse_pem(&key_pem) {
        if let Ok(PemItem::PrivateKey(k)) = item {
            client_key = Some(k);
            break;
        }
    }
    let key = client_key.context("no private key found in client key PEM")?;

    let client_id = ureq::tls::ClientCert::new_with_certs(&client_certs, key);

    let tls = ureq::tls::TlsConfig::builder()
        .root_certs(root_certs)
        .client_cert(Some(client_id))
        .build();

    Ok(ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .tls_config(tls)
            .timeout_global(Some(std::time::Duration::from_secs(60)))
            .build(),
    ))
}

/// Verify an ED25519 signature over a SHA256 digest.
///
/// The server signs the SHA256 digest of the binary (not the raw binary).
/// Both `signature_hex` and `pubkey_hex` are hex-encoded.
fn verify_signature(digest: &[u8], signature_hex: &str, pubkey_hex: &str) -> Result<()> {
    let sig_bytes = hex::decode(signature_hex).context("decode signature hex")?;
    let sig = Signature::from_slice(&sig_bytes).context("parse ED25519 signature")?;

    let pubkey_bytes = hex::decode(pubkey_hex).context("decode public key hex")?;
    let pubkey_array: [u8; 32] = pubkey_bytes
        .try_into()
        .map_err(|v: Vec<u8>| anyhow::anyhow!("public key must be 32 bytes, got {}", v.len()))?;
    let verifying_key = VerifyingKey::from_bytes(&pubkey_array)
        .context("invalid ED25519 public key")?;

    verifying_key
        .verify_strict(digest, &sig)
        .context("ED25519 signature verification failed — binary may have been tampered with")?;

    Ok(())
}

/// Fetch the server's signing public key from `/api/v1/agent/signing-key`.
fn fetch_signing_key(agent: &ureq::Agent, api_addr: &str) -> Result<String> {
    let url = format!("{}/api/v1/agent/signing-key", api_addr);
    let resp = agent
        .get(&url)
        .call()
        .map_err(|e| anyhow::anyhow!("fetch signing key: {}", e))?;

    if resp.status() != 200 {
        bail!("signing key endpoint returned status {}", resp.status());
    }

    let body = resp.into_body().read_to_vec().context("read signing key body")?;

    #[derive(serde::Deserialize)]
    struct KeyResp {
        public_key: String,
    }
    let kr: KeyResp = serde_json::from_slice(&body).context("decode signing key response")?;
    Ok(kr.public_key)
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
    tls: Option<&TlsCerts>,
) -> Result<Option<VersionInfo>> {
    let url = format!("{}/api/v1/agent/latest?os={}&arch={}", api_addr, os, arch);
    let agent = build_agent(tls);

    let resp_bytes = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let resp = agent
            .get(&url)
            .call()
            .map_err(|e| anyhow::anyhow!("version check request: {}", e))?;

        if resp.status() != 200 {
            bail!("version check returned status {}", resp.status());
        }

        resp.into_body()
            .read_to_vec()
            .context("read version response body")
    })
    .await
    .context("spawn_blocking join")??;

    let mut info: VersionInfo =
        serde_json::from_slice(&resp_bytes).context("decode version response")?;

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

/// Download the new agent binary, verify its SHA256 checksum and ED25519
/// signature, and atomically replace the current binary.
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
    tls: Option<&TlsCerts>,
    api_addr: Option<&str>,
) -> Result<()> {
    let dir = Path::new(current_binary)
        .parent()
        .unwrap_or(Path::new("/tmp"));

    // Download to temp file in same directory for atomic rename.
    let tmp_path = dir.join(format!(".vigo-update-{}", std::process::id()));
    let _cleanup = TempFileGuard(tmp_path.clone());

    let download_url = info.download_url.clone();
    let tls_certs = tls.map(|t| TlsCerts {
        ca_cert: t.ca_cert.clone(),
        client_cert: t.client_cert.clone(),
        client_key: t.client_key.clone(),
    });
    let api_addr_owned = api_addr.map(|s| s.to_string());
    let signature = info.signature.clone();

    let expected_sha = info.sha256.clone();

    let body = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let agent = build_agent(tls_certs.as_ref());

        let resp = agent
            .get(&download_url)
            .call()
            .map_err(|e| anyhow::anyhow!("download binary: {}", e))?;

        if resp.status() != 200 {
            bail!("download returned status {}", resp.status());
        }

        let body = resp.into_body()
            .read_to_vec()
            .context("download binary body")?;

        // Compute SHA256 digest.
        let digest = Sha256::digest(&body);
        let computed_hex = format!("{:x}", digest);

        if computed_hex != expected_sha {
            bail!(
                "checksum mismatch: expected {}, got {}",
                expected_sha,
                computed_hex
            );
        }

        // Verify ED25519 signature if available.
        if !signature.is_empty() {
            if let Some(ref addr) = api_addr_owned {
                match fetch_signing_key(&agent, addr) {
                    Ok(pubkey_hex) => {
                        verify_signature(&digest, &signature, &pubkey_hex)?;
                        info!("ED25519 signature verified");
                    }
                    Err(e) => {
                        warn!(error = %e, "could not fetch signing key, skipping signature verification");
                    }
                }
            }
        } else {
            debug!("no signature provided, skipping ED25519 verification");
        }

        Ok(body)
    })
    .await
    .context("spawn_blocking join")??;

    // Write temp file.
    std::fs::write(&tmp_path, &body).context("write temp binary")?;
    crate::platform::permissions::set_executable(&tmp_path)
        .context("chmod temp binary")?;

    // Atomic rename.
    std::fs::rename(&tmp_path, current_binary).context("atomic rename")?;

    info!(
        version = %info.version,
        sha256 = %info.sha256,
        "binary updated successfully"
    );

    // Refresh the systemd unit file if the bootstrap template has
    // changed since this envoy was first installed. Without this,
    // envoys bootstrapped on older agent versions never pick up new
    // unit fields — most recently `LimitNOFILE=65536`, missing on
    // hosts installed before that template change. Without the bump
    // the soft FD limit stays at the systemd default of 1024 and the
    // swarm peer server hits EMFILE under modest load.
    #[cfg(target_os = "linux")]
    {
        if std::path::Path::new("/usr/bin/systemctl").exists()
            || std::path::Path::new("/bin/systemctl").exists()
        {
            match crate::bootstrap::systemd::refresh_unit_file_if_changed().await {
                Ok(true) => info!("systemd unit refreshed for new agent version"),
                Ok(false) => debug!("systemd unit already current"),
                Err(e) => warn!(error = %e, "could not refresh systemd unit; continuing"),
            }
        }
    }

    // Exec replaces the current process.
    if exec_self {
        #[cfg(unix)]
        {
            let args: Vec<std::ffi::CString> = std::env::args()
                .map(|a| std::ffi::CString::new(a).map_err(|e| anyhow::anyhow!("invalid arg for CString: {}", e)))
                .collect::<Result<Vec<_>, _>>()?;
            let binary = std::ffi::CString::new(current_binary)
                .map_err(|e| anyhow::anyhow!("invalid binary path for CString: {}", e))?;
            nix::unistd::execv(&binary, &args).context("exec new binary")?;
        }
        #[cfg(windows)]
        {
            let args: Vec<String> = std::env::args().skip(1).collect();
            std::process::Command::new(current_binary)
                .args(&args)
                .spawn()
                .context("spawn new binary")?;
            std::process::exit(0);
        }
    }

    Ok(())
}

/// Check for a self-update and apply it if available.
///
/// Failures are logged but never crash the agent.
pub async fn check_self_update(api_addr: &str, current_version: &str, tls: Option<&TlsCerts>) {
    let os = std::env::consts::OS;
    let arch = std::env::consts::ARCH;

    match check_for_update(api_addr, current_version, os, arch, tls).await {
        Ok(Some(info)) => {
            let current_binary = match std::env::current_exe() {
                Ok(p) => p.to_string_lossy().to_string(),
                Err(e) => {
                    warn!(error = %e, "cannot determine current binary path");
                    return;
                }
            };

            // Circuit breaker: skip if the compiled-in version already matches
            // the server's advertised version. We compare version strings rather
            // than on-disk SHA256 because after a successful download + failed
            // exec(), the on-disk binary would match even though this process is
            // still running the old code.
            if info.version == current_version {
                info!(
                    version = current_version,
                    "update available but running version already matches, skipping"
                );
                return;
            }

            info!(
                current = current_version,
                new = %info.version,
                "newer agent version available, updating"
            );

            if let Err(e) = apply(&info, &current_binary, true, tls, Some(api_addr)).await {
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

/// Force a self-update regardless of version. Downloads the latest binary,
/// verifies checksum + signature, replaces the current binary, and exec restarts.
///
/// Called when the server sets force_update on the check-in response.
/// Failures are logged but never crash the agent.
///
/// Circuit breaker: if the compiled-in version already matches the server's
/// advertised version, the update is skipped to prevent an infinite
/// download-exec loop. We compare version strings (compiled into the binary
/// at build time) rather than on-disk SHA256, because after a successful
/// download + failed exec(), the on-disk binary would match even though this
/// process is still running the old code.
pub async fn force_self_update(api_addr: &str, current_version: &str, tls: Option<&TlsCerts>) {
    let os = std::env::consts::OS;
    let arch = std::env::consts::ARCH;

    match check_for_update(api_addr, "force", os, arch, tls).await {
        Ok(Some(info)) => {
            let current_binary = match std::env::current_exe() {
                Ok(p) => p.to_string_lossy().to_string(),
                Err(e) => {
                    warn!(error = %e, "cannot determine current binary path");
                    return;
                }
            };

            // Circuit breaker: compare the compiled-in version against the
            // server's advertised version. If they match, this process is
            // already running the latest code — exec-ing again would loop.
            if info.version == current_version {
                info!(
                    version = current_version,
                    "forced update: running version already matches server, skipping"
                );
                return;
            }

            info!(
                current = current_version,
                new = %info.version,
                "forced update: downloading latest binary"
            );
            if let Err(e) = apply(&info, &current_binary, true, tls, Some(api_addr)).await {
                warn!(error = %e, "forced update apply failed, continuing with current version");
            }
        }
        Ok(None) => {
            info!("forced update: server reports no update available");
        }
        Err(e) => {
            warn!(error = %e, "forced update version check failed");
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

        let info = check_for_update(&server.url(), "1.0.0", "linux", "x86_64", None)
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

        let info = check_for_update(&server.url(), "1.0.0", "linux", "x86_64", None)
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

        let info = check_for_update(&server.url(), "1.0.0", "linux", "x86_64", None)
            .await
            .unwrap();
        assert!(info.is_none());
    }

    #[test]
    fn verify_signature_valid() {
        use ed25519_dalek::{Signer, SigningKey};
        use rand::rngs::OsRng;

        let signing_key = SigningKey::generate(&mut OsRng);
        let verifying_key = signing_key.verifying_key();

        let digest = Sha256::digest(b"test binary content");
        let sig = signing_key.sign(&digest);

        let sig_hex = hex::encode(sig.to_bytes());
        let pubkey_hex = hex::encode(verifying_key.to_bytes());

        verify_signature(&digest, &sig_hex, &pubkey_hex).unwrap();
    }

    #[test]
    fn verify_signature_invalid() {
        use ed25519_dalek::{Signer, SigningKey};
        use rand::rngs::OsRng;

        let signing_key = SigningKey::generate(&mut OsRng);
        let wrong_key = SigningKey::generate(&mut OsRng);
        let wrong_verifying = wrong_key.verifying_key();

        let digest = Sha256::digest(b"test binary content");
        let sig = signing_key.sign(&digest);

        let sig_hex = hex::encode(sig.to_bytes());
        let pubkey_hex = hex::encode(wrong_verifying.to_bytes());

        assert!(verify_signature(&digest, &sig_hex, &pubkey_hex).is_err());
    }

    #[test]
    fn verify_signature_tampered_digest() {
        use ed25519_dalek::{Signer, SigningKey};
        use rand::rngs::OsRng;

        let signing_key = SigningKey::generate(&mut OsRng);
        let verifying_key = signing_key.verifying_key();

        let digest = Sha256::digest(b"original content");
        let sig = signing_key.sign(&digest);

        let tampered_digest = Sha256::digest(b"tampered content");

        let sig_hex = hex::encode(sig.to_bytes());
        let pubkey_hex = hex::encode(verifying_key.to_bytes());

        assert!(verify_signature(&tampered_digest, &sig_hex, &pubkey_hex).is_err());
    }
}
