// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigocrypt content pusher.
//!
//! Consumes `PushJob` events from the watcher and POSTs each
//! ciphertext blob to:
//!
//! 1. Every live peer in `peers.json` for the job's user, using the
//!    peer's `last_seen_addr` (captured when the announcement arrived).
//! 2. Every `extra_peers:` hostname listed in the user's
//!    `~/.vigocrypt/config.yaml`, resolved via DNS.
//!
//! Uses a dedicated `ureq::Agent` with a 5-minute global timeout —
//! long enough for a 1 GiB push over a reasonable network, but
//! bounded so a stuck connection can't pin the blocking pool
//! forever. Pushes run in `spawn_blocking` so the tokio runtime
//! stays responsive.
//!
//! No retry on failure: the recipient will observe the blob missing
//! on its next announcement cycle and pull via
//! `GET /vigocrypt/pull/<user>/<filename>`. The pusher's job is
//! best-effort proactive delivery, not guaranteed transport.

use std::time::Duration;

use anyhow::{Context, Result};
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use crate::update::TlsCerts;
use super::config;
use super::state::{PeerTable, STALE_SECS};
use super::watcher::PushJob;

/// Per-push HTTP timeout. Allows a 1 GiB push over ~30 Mbps without
/// timing out; shorter pushes finish well under this.
const PUSH_TIMEOUT: Duration = Duration::from_secs(300);

/// Max concurrent pushes across all peers. Bounded so a fan-out to
/// a large fleet doesn't starve the blocking threadpool.
const MAX_INFLIGHT: usize = 4;

/// Spawn the pusher. Drains `jobs` until cancelled.
pub fn spawn(
    tls: TlsCerts,
    mut jobs: mpsc::Receiver<PushJob>,
    cancel: CancellationToken,
) {
    tokio::spawn(async move {
        let agent = match build_agent(&tls) {
            Ok(a) => a,
            Err(e) => {
                warn!(error = %e, "vigocrypt pusher: cannot build HTTPS agent; fan-out disabled");
                return;
            }
        };
        info!("vigocrypt pusher started");

        let sem = std::sync::Arc::new(tokio::sync::Semaphore::new(MAX_INFLIGHT));
        loop {
            tokio::select! {
                _ = cancel.cancelled() => {
                    info!("vigocrypt pusher: cancelled, exiting");
                    return;
                }
                maybe = jobs.recv() => {
                    let job = match maybe {
                        Some(j) => j,
                        None => return,
                    };
                    let agent_clone = agent.clone();
                    let sem_clone = sem.clone();
                    tokio::spawn(async move {
                        let _permit = sem_clone.acquire_owned().await.ok();
                        if let Err(e) = fan_out(&agent_clone, job).await {
                            debug!(error = %e, "vigocrypt pusher: fan_out error");
                        }
                    });
                }
            }
        }
    });
}

async fn fan_out(agent: &ureq::Agent, job: PushJob) -> Result<()> {
    let PushJob { user, filename, bytes } = job;

    // Collect targets: LAN peers from peers.json + extra_peers from
    // the user's config.yaml.
    let lan_targets = lan_targets_for(&user);
    let extra_targets = extra_targets_for(&user);
    let total = lan_targets.len() + extra_targets.len();
    if total == 0 {
        debug!(user = %user, filename = %filename, "pusher: no targets; nothing to push");
        return Ok(());
    }
    info!(
        user = %user,
        filename = %filename,
        size_bytes = bytes.len(),
        lan = lan_targets.len(),
        extra = extra_targets.len(),
        "pusher: fanning out",
    );

    // Fan out. Each POST runs in spawn_blocking so the ureq call
    // (blocking) doesn't tie up the tokio runtime.
    let mut handles = Vec::with_capacity(total);
    for target in lan_targets.into_iter().chain(extra_targets) {
        let agent = agent.clone();
        let user = user.clone();
        let filename = filename.clone();
        let bytes = bytes.clone();
        handles.push(tokio::task::spawn_blocking(move || {
            push_one(&agent, &target, &user, &filename, &bytes)
        }));
    }
    for h in handles {
        match h.await {
            Ok(Ok(())) => {}
            Ok(Err(e)) => warn!(error = %e, "pusher: peer push failed"),
            Err(e) => warn!(error = %e, "pusher: spawn_blocking join failed"),
        }
    }
    Ok(())
}

/// One push attempt. Blocking; runs inside `spawn_blocking`.
fn push_one(
    agent: &ureq::Agent,
    target: &PushTarget,
    user: &str,
    filename: &str,
    bytes: &[u8],
) -> Result<()> {
    let url = format!(
        "https://{}/vigocrypt/push/{}/{}",
        target.addr,
        urlencode(user),
        urlencode(filename)
    );
    let resp = agent
        .post(&url)
        .header("Content-Type", "application/octet-stream")
        .send(bytes.to_vec())
        .with_context(|| format!("POST {}", url))?;
    let status = resp.status();
    if !status.is_success() {
        anyhow::bail!("peer {} returned HTTP {}", target.label, status.as_u16());
    }
    debug!(
        target = %target.label,
        user = %user,
        filename = %filename,
        "pusher: pushed"
    );
    Ok(())
}

#[derive(Debug, Clone)]
struct PushTarget {
    /// "<ip>:1531" or "<host>:1531" — what goes into the URL.
    addr: String,
    /// Human-readable label for logs.
    label: String,
}

fn lan_targets_for(user: &str) -> Vec<PushTarget> {
    let table = PeerTable::load(user);
    let now = super::now_secs_pub();
    let mut out = Vec::new();
    for (host, peer) in &table.peers {
        if now - peer.last_seen > STALE_SECS {
            continue;
        }
        let addr = match &peer.last_seen_addr {
            Some(a) => a.clone(),
            None => continue, // can't push without a known IP
        };
        // Strip any port the SocketAddr display included — we
        // always target the peer server on 1531, not the UDP
        // announce source port.
        let host_only = strip_port(&addr);
        out.push(PushTarget {
            addr: format!("{}:{}", host_only, PEER_PORT),
            label: host.clone(),
        });
    }
    out
}

fn extra_targets_for(user: &str) -> Vec<PushTarget> {
    let home = match super::home_for_user_pub(user) {
        Some(h) => h,
        None => return Vec::new(),
    };
    let cfg = config::load_config(&home);
    cfg.extra_peers
        .into_iter()
        .map(|h| PushTarget {
            addr: format!("{}:{}", h, PEER_PORT),
            label: h,
        })
        .collect()
}

const PEER_PORT: u16 = 1531;

fn strip_port(addr: &str) -> String {
    // For IPv4 "1.2.3.4:1234" or IPv6 "[::1]:1234". Use the last ':'
    // for IPv4 and the right bracket for IPv6.
    if let Some(rb) = addr.rfind(']') {
        // IPv6 literal — keep brackets, drop port.
        return addr[..=rb].to_string();
    }
    match addr.rfind(':') {
        Some(i) => addr[..i].to_string(),
        None => addr.to_string(),
    }
}

fn urlencode(s: &str) -> String {
    // Percent-encode anything that isn't an unreserved URL char.
    let mut out = String::with_capacity(s.len());
    for b in s.as_bytes() {
        match *b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(*b as char);
            }
            _ => {
                out.push_str(&format!("%{:02X}", b));
            }
        }
    }
    out
}

/// Exposed to mod.rs for the extra_peers announce loop, which needs
/// the same TLS-configured agent.
pub(crate) fn build_agent_for_mod(tls: &TlsCerts) -> Result<ureq::Agent> {
    build_agent(tls)
}

/// Build the mTLS-configured ureq agent used for vigocrypt pushes.
/// Separate from swarm::peer_client's agent because we need a
/// longer timeout (5 min vs. 30 s) to accommodate 1 GiB blobs.
fn build_agent(tls: &TlsCerts) -> Result<ureq::Agent> {
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
        .disable_verification(true)
        .build();

    Ok(ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .tls_config(tls_cfg)
            .timeout_connect(Some(Duration::from_secs(5)))
            .timeout_global(Some(PUSH_TIMEOUT))
            .timeout_send_body(Some(PUSH_TIMEOUT))
            .build(),
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn strip_port_ipv4() {
        assert_eq!(strip_port("1.2.3.4:1234"), "1.2.3.4");
        assert_eq!(strip_port("1.2.3.4"), "1.2.3.4");
    }

    #[test]
    fn strip_port_ipv6() {
        assert_eq!(strip_port("[::1]:1234"), "[::1]");
        assert_eq!(strip_port("[fe80::1]:9999"), "[fe80::1]");
    }

    #[test]
    fn urlencode_preserves_unreserved() {
        assert_eq!(urlencode("dan"), "dan");
        assert_eq!(urlencode("my-file.pdf"), "my-file.pdf");
        assert_eq!(urlencode("with space"), "with%20space");
        assert_eq!(urlencode("slash/here"), "slash%2Fhere");
    }
}
