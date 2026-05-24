// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Agent self-update mechanism: version checking against the server, binary
//! download with SHA256 + ED25519 signature verification, atomic replacement,
//! and process restart.
//!
//! The byte-fetch step is routed through [`curator_accelerator`] first (ADR-024
//! slice 8) — when the operator has published the agent as a curator artifact,
//! envoys with the matching platform's blob in their local swarm cache get the
//! bytes P2P instead of from the server. The REST `download_url` is the
//! always-on fallback and the authoritative answer; curator never replaces
//! `/api/v1/agent/latest`. The SHA256 + ED25519 signature verification runs
//! identically on whichever path produced the bytes.

pub mod curator_accelerator;

use anyhow::{bail, Context, Result};
use ed25519_dalek::{Signature, VerifyingKey};
use sha2::{Digest, Sha256};
use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};
use tracing::{debug, info, warn};

/// Suffix the Linux kernel appends to /proc/PID/exe symlink targets when the
/// originally-loaded inode has been unlinked from the filesystem. See
/// proc(5): "If the pathname has been unlinked, the symlink will contain the
/// string '(deleted)' appended to the original pathname."
const DELETED_SUFFIX: &str = " (deleted)";

/// Track whether we've already logged a `(deleted)` recovery for this
/// process. The condition is sticky for the life of the process — every
/// `current_exe()` call returns the same value — so logging on every
/// update attempt would flood the journal. Reset implicitly on execv.
static DELETED_SUFFIX_LOGGED: AtomicBool = AtomicBool::new(false);

/// Resolve the path to the running agent binary, defending against the
/// case where the running process's exec inode has been unlinked from its
/// original path. Returns the canonical (suffix-stripped) path.
///
/// Without this, `std::env::current_exe()` returns a literal string with
/// the kernel's `(deleted)` suffix, which then gets used as a destination
/// path for the next update — writing the new binary to a phantom file
/// while leaving the real `/usr/local/sbin/vigo` (or wherever) untouched
/// indefinitely. We hit this on danlap pre-0.39.3 when the agent's own
/// startup self-update wrote a new binary on top of itself before its
/// version probe could reject the update; the rejection path returned
/// early after the rename was already committed, so the running process's
/// exec inode was orphaned for the rest of the process's lifetime.
///
/// The 0.39.3 rendering of `apply()` probes the temp file BEFORE renaming,
/// so new occurrences should not happen. This helper handles legacy state
/// from already-affected hosts so they recover automatically on the next
/// update attempt without an operator restart.
fn resolve_current_binary() -> Result<String> {
    let raw = std::env::current_exe()
        .context("std::env::current_exe()")?
        .to_string_lossy()
        .to_string();
    if let Some(stripped) = raw.strip_suffix(DELETED_SUFFIX) {
        if !DELETED_SUFFIX_LOGGED.swap(true, Ordering::Relaxed) {
            info!(
                raw_exe = %raw,
                stripped = %stripped,
                "agent exec inode is unlinked from filesystem (kernel-rendered ' (deleted)' suffix on /proc/PID/exe). Stripping suffix; the upcoming update will replace the on-disk binary at the canonical path and execv into it cleanly. Subsequent occurrences in this process lifetime will be silent."
            );
        }
        Ok(stripped.to_string())
    } else {
        Ok(raw)
    }
}

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

/// Baked-in ED25519 pubkey used to verify agent self-updates. Injected
/// at build time from `VIGO_AGENT_SIGNING_PUBKEY` (see agent/build.rs);
/// the corresponding private key (`SIGN_KEY` in the Makefile) signs the
/// distributed `.sig` files. Verification uses this constant exclusively
/// — the agent does NOT trust whatever pubkey the server returns at
/// update time, because a compromised server hands out its own.
const SIGNING_PUBKEY_HEX: &str = env!("VIGO_AGENT_SIGNING_PUBKEY");

/// The dev sentinel: an agent built without a real `SIGN_KEY` in scope
/// (plain `cargo build` / `cargo test`) ships with this all-zero pubkey
/// baked in. At runtime, `verify_signature_or_bail` refuses to proceed
/// against the sentinel, with an operator-actionable error pointing at
/// `make build-agent SIGN_KEY=<path>`. Build succeeds so the wider
/// cargo workflow isn't broken; safety comes from the runtime refusal.
const SIGNING_PUBKEY_DEV_SENTINEL: &str =
    "0000000000000000000000000000000000000000000000000000000000000000";

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

/// Fail-closed update verification: refuse to install on missing
/// signature, on the dev-sentinel pubkey, or on signature/pubkey
/// mismatch. Honors the CLAUDE.md guarantee that the agent self-updates
/// only from ed25519-signed binaries (ADR-031).
fn verify_signature_or_bail(digest: &[u8], signature_hex: &str, pubkey_hex: &str) -> Result<()> {
    if signature_hex.is_empty() {
        bail!("server response missing ED25519 signature — refusing to install unsigned binary (CLAUDE.md self-update guarantee, ADR-031)");
    }
    if pubkey_hex == SIGNING_PUBKEY_DEV_SENTINEL {
        bail!("agent built without signing key (dev sentinel pubkey baked in) — rebuild via `make build-agent SIGN_KEY=<path>` before enabling self-update");
    }
    verify_signature(digest, signature_hex, pubkey_hex)
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
    let _ = api_addr; // baked-in pubkey replaced fetch_signing_key (ADR-031)
    let signature = info.signature.clone();

    let expected_sha = info.sha256.clone();
    let version_for_curator = info.version.clone();
    // Translate Rust's OS/arch constants into the catalog's GNU-style vocab
    // for the curator artifact lookup ("linux"/"darwin"/"windows", "amd64"/
    // "arm64"). Apply() is always called inside the running agent so
    // std::env::consts is exact for the host we're updating.
    let curator_os = match std::env::consts::OS {
        "macos" => "darwin".to_string(),
        other => other.to_string(),
    };
    let curator_arch = match std::env::consts::ARCH {
        "x86_64" => "amd64".to_string(),
        "aarch64" => "arm64".to_string(),
        other => other.to_string(),
    };

    let body = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let agent = build_agent(tls_certs.as_ref());

        // ADR-024 slice 8: try curator first. The SHA256 + signature gate
        // below runs identically on whichever path produced the bytes; the
        // REST `download_url` is the always-on fallback and the authority.
        let body = if let Some(bytes) = curator_accelerator::fetch_via_curator(
            &version_for_curator,
            &expected_sha,
            &curator_os,
            &curator_arch,
        ) {
            bytes
        } else {
            let resp = agent
                .get(&download_url)
                .call()
                .map_err(|e| anyhow::anyhow!("download binary: {}", e))?;

            if resp.status() != 200 {
                bail!("download returned status {}", resp.status());
            }

            resp.into_body()
                .read_to_vec()
                .context("download binary body")?
        };

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

        // Fail-closed ED25519 verification against the baked-in pubkey
        // (ADR-031). Empty signature, sentinel pubkey, or mismatch all
        // bail — the agent never installs an unverified binary.
        verify_signature_or_bail(&digest, &signature, SIGNING_PUBKEY_HEX)?;
        info!("ED25519 signature verified");

        Ok(body)
    })
    .await
    .context("spawn_blocking join")??;

    // Write temp file.
    std::fs::write(&tmp_path, &body).context("write temp binary")?;
    crate::platform::permissions::set_executable(&tmp_path)
        .context("chmod temp binary")?;

    // Pre-rename version probe. Run the TMP file with `version` and confirm
    // its compiled-in version matches what the server advertised. This is
    // the agent-side guard against the cross-arch update loop bug — if the
    // server's dist tree has a binary labeled X but the binary itself was
    // built at version Y, our next check-in would still report Y, the
    // server would still want X, and we'd loop forever on every iteration.
    //
    // This MUST run before the atomic rename (pre-0.39.3 it ran after, which
    // meant a mismatch left the on-disk binary already replaced. The running
    // process didn't execv, so its loaded inode was orphaned from the
    // filesystem — `/proc/PID/exe` then rendered with the kernel's
    // `(deleted)` suffix, and `current_exe()` returned that suffixed string,
    // poisoning every subsequent update target until process restart). With
    // the probe before rename, an aborted update leaves on-disk state
    // completely untouched (TempFileGuard cleans up the tmp file).
    if !info.version.is_empty() {
        match std::process::Command::new(&tmp_path).arg("version").output() {
            Ok(out) if out.status.success() => {
                let probed = String::from_utf8_lossy(&out.stdout).trim().to_string();
                if !probed.is_empty() && probed != info.version {
                    warn!(
                        advertised = %info.version,
                        actual = %probed,
                        sha256 = %info.sha256,
                        tmp_path = %tmp_path.display(),
                        "version mismatch on downloaded binary — server advertised one version, the binary's compiled-in version is another. Aborting update before rename so the on-disk binary is untouched. Likely cause: cross-arch labeling bug in the server's dist tree (each arch's vigo.version sidecar must match its binary)."
                    );
                    return Ok(());
                }
            }
            Ok(out) => {
                debug!(
                    status = ?out.status,
                    "pre-rename version probe exited non-zero — proceeding with rename + exec (binary may need first-run init)"
                );
            }
            Err(e) => {
                debug!(error = %e, "pre-rename version probe failed — proceeding with rename + exec");
            }
        }
    }

    // Atomic rename — only fires after the probe passes (or no info.version
    // was provided), so an aborted update never replaces the on-disk binary.
    std::fs::rename(&tmp_path, current_binary).context("atomic rename")?;

    info!(
        version = %info.version,
        sha256 = %info.sha256,
        "binary updated successfully"
    );

    // Re-assert the `git-remote-gitback` PATH symlink. `bootstrap`
    // installs it, but a self-update is a separate code path — without
    // this an envoy that came back via the update path (rather than a
    // fresh bootstrap) loses the helper, so `git push gitback://`
    // breaks. Best-effort, mirrors the bootstrap install paths. Also
    // sweeps the pre-rename `git-remote-vigo` symlink.
    #[cfg(unix)]
    {
        if let Err(e) = crate::bootstrap::ensure_git_remote_gitback_symlink(current_binary) {
            warn!(
                error = %e,
                link = crate::bootstrap::GIT_REMOTE_GITBACK_SYMLINK,
                "could not refresh git-remote-gitback symlink; gitback:// git URLs unavailable until fixed"
            );
        }
    }

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
            let current_binary = match resolve_current_binary() {
                Ok(p) => p,
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
            let current_binary = match resolve_current_binary() {
                Ok(p) => p,
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
    fn deleted_suffix_strips_only_at_end() {
        // Direct strip-suffix semantics — proves we won't accidentally
        // chop a path whose middle happens to contain " (deleted)".
        let canonical = "/usr/local/sbin/vigo";
        let suffixed = format!("{}{}", canonical, super::DELETED_SUFFIX);
        assert_eq!(
            suffixed.strip_suffix(super::DELETED_SUFFIX),
            Some(canonical)
        );

        // Path ending in a different suffix → no strip.
        let unrelated = "/usr/local/sbin/vigo (whatever)";
        assert_eq!(unrelated.strip_suffix(super::DELETED_SUFFIX), None);

        // Path containing " (deleted)" mid-string → no strip (only end matches).
        let middle = "/some (deleted) path/vigo";
        assert_eq!(middle.strip_suffix(super::DELETED_SUFFIX), None);
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

    // ADR-031 fail-closed verification (verify_signature_or_bail): empty
    // signature, the dev sentinel pubkey, and signature/pubkey mismatch
    // all bail. The wrapper exists so this contract is testable without
    // mutating the compile-time-const baked-in pubkey.

    #[test]
    fn verify_signature_or_bail_bails_on_empty_signature() {
        use ed25519_dalek::SigningKey;
        use rand::rngs::OsRng;
        let pubkey_hex = hex::encode(SigningKey::generate(&mut OsRng).verifying_key().to_bytes());
        let digest = Sha256::digest(b"any binary");
        let err = verify_signature_or_bail(&digest, "", &pubkey_hex).unwrap_err();
        assert!(err.to_string().contains("missing ED25519 signature"), "got: {err}");
    }

    #[test]
    fn verify_signature_or_bail_bails_on_sentinel_pubkey() {
        let digest = Sha256::digest(b"any binary");
        // Any non-empty signature; sentinel-pubkey check fires before
        // signature parsing.
        let err = verify_signature_or_bail(&digest, "deadbeef", SIGNING_PUBKEY_DEV_SENTINEL)
            .unwrap_err();
        assert!(
            err.to_string().contains("dev sentinel"),
            "got: {err}"
        );
    }

    #[test]
    fn verify_signature_or_bail_passes_on_valid_signature() {
        use ed25519_dalek::{Signer, SigningKey};
        use rand::rngs::OsRng;
        let signing_key = SigningKey::generate(&mut OsRng);
        let pubkey_hex = hex::encode(signing_key.verifying_key().to_bytes());
        let digest = Sha256::digest(b"valid binary");
        let sig_hex = hex::encode(signing_key.sign(&digest).to_bytes());
        verify_signature_or_bail(&digest, &sig_hex, &pubkey_hex).unwrap();
    }

    #[test]
    fn verify_signature_or_bail_fails_on_wrong_pubkey() {
        use ed25519_dalek::{Signer, SigningKey};
        use rand::rngs::OsRng;
        let signing_key = SigningKey::generate(&mut OsRng);
        let wrong_pubkey_hex = hex::encode(
            SigningKey::generate(&mut OsRng).verifying_key().to_bytes(),
        );
        let digest = Sha256::digest(b"valid binary");
        let sig_hex = hex::encode(signing_key.sign(&digest).to_bytes());
        assert!(verify_signature_or_bail(&digest, &sig_hex, &wrong_pubkey_hex).is_err());
    }
}
