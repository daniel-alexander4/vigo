// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Periodic fetcher for the fleet friendly-name map (ADR-022).
//!
//! Pulls `GET /api/v1/swarm/puddle/names` from the server on a 15-min
//! timer plus a one-shot at agent startup, and writes the result to
//! [`super::names_cache::names_cache_path`]
//! (`/var/lib/vigo/swarm/puddle/names.json`, world-readable). Three
//! consumers read that cache — none on the check-in path:
//!
//! - `vigo swarm puddle name set` (refuse-the-collision check),
//! - the `git-remote-swarmgit` helper daemon (`<name>` → project_id),
//! - `vigo swarm puddle name show`.
//!
//! The endpoint carries only public-by-construction name↔pubkey
//! bindings, so — like `/api/v1/fleet/pubkeys` — it's fetched over
//! mTLS without a token. Transient failures log WARN and leave the
//! last good cache in place; name resolution then just falls back to
//! the hex `swarmgit://<project_id>/<repo>` form until the next
//! refresh. Mirrors [`crate::swarm::swarmgit::pubkey_bootstrap`].

use std::path::Path;
use std::time::Duration;

use anyhow::{anyhow, bail, Context, Result};
use tokio::time::MissedTickBehavior;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use crate::update::TlsCerts;

/// Refresh cadence — same as the swarmgit pubkey bootstrap.
const REFRESH_INTERVAL: Duration = Duration::from_secs(15 * 60);

/// Per-request timeout — the response is a small JSON map.
const FETCH_TIMEOUT: Duration = Duration::from_secs(15);

/// Spawn the periodic fetch+write loop. Returns immediately. Performs
/// one fetch on entry so the cache is populated before the first
/// `swarmgit://<name>/<repo>` clone; failures are logged and tolerated
/// (resolution falls back to the hex form).
pub fn spawn(server_addr: String, tls: TlsCerts, cancel: CancellationToken) {
    tokio::spawn(async move {
        if let Err(e) = fetch_and_write(&server_addr, &tls).await {
            warn!(error = %e, "puddle name-map bootstrap: initial fetch failed; friendly-name resolution falls back to hex URLs until next tick");
        }
        let mut interval = tokio::time::interval(REFRESH_INTERVAL);
        interval.set_missed_tick_behavior(MissedTickBehavior::Skip);
        // First tick fires immediately — consume it (we already fetched).
        interval.tick().await;
        loop {
            tokio::select! {
                _ = cancel.cancelled() => {
                    info!("puddle name-map bootstrap: cancelled, exiting");
                    return;
                }
                _ = interval.tick() => {
                    if let Err(e) = fetch_and_write(&server_addr, &tls).await {
                        warn!(error = %e, "puddle name-map bootstrap: periodic fetch failed");
                    }
                }
            }
        }
    });
}

async fn fetch_and_write(server_addr: &str, tls: &TlsCerts) -> Result<()> {
    let url = format!(
        "{}/api/v1/swarm/puddle/names",
        server_addr.trim_end_matches('/')
    );
    let agent = build_mtls_agent(tls)?;

    let body = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let resp = agent
            .get(&url)
            .call()
            .map_err(|e| anyhow!("puddle names request: {}", e))?;
        if resp.status() != 200 {
            bail!("puddle names endpoint returned status {}", resp.status());
        }
        resp.into_body()
            .read_to_vec()
            .context("read puddle names response body")
    })
    .await
    .context("spawn_blocking join")??;

    // Validate it parses before persisting — don't replace a good
    // cache with garbage on a server-side bug.
    let parsed: super::names_cache::FleetNameMap =
        serde_json::from_slice(&body).context("decode puddle names JSON")?;

    let path = super::names_cache::names_cache_path();
    write_world_readable(&path, &body).with_context(|| format!("writing {}", path.display()))?;
    info!(
        names = parsed.names.len(),
        path = %path.display(),
        "puddle name-map bootstrap: refreshed local cache"
    );
    Ok(())
}

#[cfg(unix)]
fn write_world_readable(path: &Path, content: &[u8]) -> Result<()> {
    use std::io::Write;
    use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
    if let Some(dir) = path.parent() {
        if !dir.is_dir() {
            std::fs::DirBuilder::new()
                .recursive(true)
                .mode(0o755)
                .create(dir)
                .with_context(|| format!("creating {}", dir.display()))?;
        }
    }
    let tmp = path.with_extension("json.tmp");
    {
        let mut f = std::fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o644)
            .open(&tmp)
            .with_context(|| format!("creating {}", tmp.display()))?;
        f.write_all(content)?;
        f.sync_all().ok();
    }
    std::fs::rename(&tmp, path)
        .with_context(|| format!("rename {} -> {}", tmp.display(), path.display()))?;
    Ok(())
}

#[cfg(not(unix))]
fn write_world_readable(path: &Path, content: &[u8]) -> Result<()> {
    if let Some(dir) = path.parent() {
        let _ = std::fs::create_dir_all(dir);
    }
    std::fs::write(path, content).with_context(|| format!("writing {}", path.display()))
}

/// Build an mTLS ureq agent for talking to the server. Same shape as
/// `swarmgit::pubkey_bootstrap::build_mtls_agent` (small JSON
/// endpoint, short global timeout); hostname verification stays on —
/// the agent talks to its configured server.
fn build_mtls_agent(tls: &TlsCerts) -> Result<ureq::Agent> {
    use ureq::tls::{parse_pem, PemItem};

    let ca_pem = std::fs::read(&tls.ca_cert).context("read CA cert")?;
    let cert_pem = std::fs::read(&tls.client_cert).context("read client cert")?;
    let key_pem = std::fs::read(&tls.client_key).context("read client key")?;

    let mut ca_certs = Vec::new();
    for item in parse_pem(&ca_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            ca_certs.push(c);
        }
    }
    let root_certs = ureq::tls::RootCerts::new_with_certs(&ca_certs);

    let mut client_certs = Vec::new();
    for item in parse_pem(&cert_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            client_certs.push(c);
        }
    }

    let mut client_key = None;
    for item in parse_pem(&key_pem) {
        if let Ok(PemItem::PrivateKey(k)) = item {
            client_key = Some(k);
            break;
        }
    }
    let key = client_key.context("no private key found in client key PEM")?;

    let client_id = ureq::tls::ClientCert::new_with_certs(&client_certs, key);
    let tls_cfg = ureq::tls::TlsConfig::builder()
        .root_certs(root_certs)
        .client_cert(Some(client_id))
        .build();

    Ok(ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .tls_config(tls_cfg)
            .timeout_global(Some(FETCH_TIMEOUT))
            .build(),
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    #[test]
    fn write_world_readable_roundtrips_and_creates_dir() {
        let dir = tempdir().unwrap();
        let path = dir.path().join("sub/names.json");
        write_world_readable(&path, b"{\"updated_at\":1,\"names\":{}}").unwrap();
        let got = std::fs::read(&path).unwrap();
        assert_eq!(got, b"{\"updated_at\":1,\"names\":{}}");
        // Overwrite works (temp+rename).
        write_world_readable(&path, b"{\"updated_at\":2,\"names\":{}}").unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), b"{\"updated_at\":2,\"names\":{}}");
    }
}
