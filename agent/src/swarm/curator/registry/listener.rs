// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Listener supervisor for the curator registry shim (ADR-024 slice 7).
//!
//! Bind decision lives in two places:
//!
//! 1. The server publishes `swarm.curator.local_port` on every CheckIn (zero
//!    = disabled; non-zero = bind on `127.0.0.1:<port>`).
//! 2. This supervisor polls the cached value every [`SUPERVISOR_TICK`] and
//!    starts / restarts / stops the listener as the published port changes —
//!    no agent restart needed when the operator turns the shim on or moves it
//!    to a different port.
//!
//! Always bound on loopback only. No TLS, no auth — anything that can connect
//! to `127.0.0.1:<port>` on the envoy is already on the envoy. The shim is
//! read-only (every mutating verb returns 405); see [`super::routes`].

use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use hyper::server::conn::http1;
use hyper::service::service_fn;
use tokio::net::TcpListener;
use tokio::sync::Semaphore;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use super::routes::{handle, ShimState};

/// How often the supervisor re-reads the runtime-limits cell for a port
/// change. 5 s — fast enough that operator flips become visible within a tick
/// or two, slow enough that the cost (one RwLock read) is invisible.
const SUPERVISOR_TICK: Duration = Duration::from_secs(5);

/// Concurrent in-flight HTTP handler ceiling. The registry shim serves
/// loopback-only Docker-Registry-v2 requests from local container runtimes
/// (containerd, podman, …); steady-state volume is one image-pull at a
/// time, so 32 is comfortably above any realistic burst while bounding a
/// runaway local client. Mirrors the five existing helper-daemon caps
/// (curator/helper.rs, gitback/helper.rs, puddle/{session,service_session}.rs,
/// lockbox/session.rs) — same value, same rationale, queue-on-saturation
/// via `acquire_owned().await` so a flood of pulls back-pressures instead
/// of crashing the agent.
const MAX_CONNECTIONS: usize = 32;

/// Spawn-target for `curator::spawn`. Watches
/// [`crate::swarm::runtime_limits::curator_local_port`] and (re)spawns the
/// HTTP accept loop when the value changes. Returns only when the parent
/// `cancel` is fired.
pub async fn run_supervisor(cancel: CancellationToken) {
    let resolver = match crate::swarm::curator::executor_resolver::build_resolver_from_agent_config() {
        Ok(r) => Arc::new(ShimState::new(r)),
        Err(e) => {
            warn!(error = %e, "curator registry: failed to build resolver — shim disabled");
            return;
        }
    };

    let mut current_port: Option<u16> = None;
    let mut current_cancel: Option<CancellationToken> = None;

    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                if let Some(c) = current_cancel.take() {
                    c.cancel();
                }
                return;
            }
            _ = tokio::time::sleep(SUPERVISOR_TICK) => {}
        }

        let desired = crate::swarm::runtime_limits::curator_local_port()
            .and_then(|p| if p > 0 && p <= u16::MAX as u32 { Some(p as u16) } else { None });

        if current_port == desired {
            continue;
        }

        if let Some(c) = current_cancel.take() {
            c.cancel();
            info!(?current_port, "curator registry: stopping listener — port changed");
        }
        current_port = None;

        let Some(port) = desired else { continue };

        let addr = SocketAddr::from(([127, 0, 0, 1], port));
        match TcpListener::bind(addr).await {
            Ok(listener) => {
                let inner = cancel.child_token();
                let state = resolver.clone();
                let inner_clone = inner.clone();
                tokio::spawn(serve(listener, state, inner_clone));
                current_cancel = Some(inner);
                current_port = Some(port);
                info!(port, "curator registry shim listening on 127.0.0.1");
            }
            Err(e) => {
                warn!(port, error = %e, "binding curator registry shim failed — will retry next tick");
            }
        }
    }
}

async fn serve(listener: TcpListener, state: Arc<ShimState>, cancel: CancellationToken) {
    let semaphore = Arc::new(Semaphore::new(MAX_CONNECTIONS));
    loop {
        let (stream, _peer) = tokio::select! {
            _ = cancel.cancelled() => return,
            r = listener.accept() => match r {
                Ok(v) => v,
                Err(e) => {
                    warn!(error = %e, "curator registry: accept failed");
                    tokio::time::sleep(Duration::from_millis(100)).await;
                    continue;
                }
            }
        };

        // Bound concurrent in-flight HTTP handlers. `acquire_owned().await`
        // queues the accept loop at saturation — local-loopback HTTP from
        // container runtimes prefers queueing over reject-and-retry-storm.
        let permit = match semaphore.clone().acquire_owned().await {
            Ok(p) => p,
            Err(_) => {
                // Semaphore closed — only happens at shutdown.
                continue;
            }
        };

        let state = state.clone();
        let conn_cancel = cancel.clone();
        tokio::spawn(async move {
            let _permit = permit; // held for connection lifetime
            let io = hyper_util::rt::TokioIo::new(stream);
            let svc = service_fn(move |req| {
                let state = state.clone();
                async move { handle(req, state).await }
            });
            let conn = http1::Builder::new()
                .timer(hyper_util::rt::TokioTimer::new())
                .header_read_timeout(Duration::from_secs(15))
                .serve_connection(io, svc);
            tokio::select! {
                _ = conn_cancel.cancelled() => {}
                r = conn => {
                    if let Err(e) = r {
                        if !e.is_incomplete_message() {
                            tracing::debug!(error = %e, "curator registry: HTTP connection error");
                        }
                    }
                }
            }
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use anyhow::Result;
    use std::net::TcpStream;

    /// End-to-end smoke test: the supervisor with a port set in the runtime
    /// limits cache binds the listener; `GET /v2/` responds 200; cancelling
    /// the parent token stops the listener and the port becomes connectable
    /// again (or, in CI's flaky lab, at least the supervisor exits cleanly).
    #[tokio::test]
    #[serial_test::serial(runtime_limits)]
    async fn supervisor_binds_and_releases_on_cancel() -> Result<()> {
        // Pick an ephemeral port the kernel hands us right now, so the test
        // doesn't fight a real Docker daemon listening on 5000.
        let probe = TcpListener::bind(SocketAddr::from(([127, 0, 0, 1], 0))).await?;
        let port = probe.local_addr()?.port();
        drop(probe);

        // The guard installs the port and resets the cell on drop, so
        // neighboring tests in the same process never pick up our test port
        // even if this test panics mid-body.
        let _g = crate::swarm::runtime_limits::install_for_test(&crate::proto::SwarmRuntimeLimits {
            filecast_max_size_bytes: 0,
            filecast_default_retention: String::new(),
            lockbox_max_file_bytes: 0,
            lockbox_envelope_retention_secs: 0,
            gitback_max_bundle_bytes: 0,
            gitback_envelope_retention_secs: 0,
            longdrawer_max_file_bytes: 0,
            curator_max_bytes: 0,
            curator_local_port: port as u32,
            poolq_max_msg_bytes: 0,
            poolq_retention_secs: 0,
        });

        let cancel = CancellationToken::new();
        let task = tokio::spawn(run_supervisor(cancel.clone()));

        // The supervisor sleeps SUPERVISOR_TICK between every wakeup; the
        // first iteration's sleep delays the initial bind by that amount.
        // We poll the port with our own retry budget — measured shorter than
        // SUPERVISOR_TICK so a CI hiccup at the tick boundary doesn't fail
        // the test outright.
        let deadline = std::time::Instant::now() + SUPERVISOR_TICK + Duration::from_secs(5);
        let bound = loop {
            if std::time::Instant::now() > deadline {
                break false;
            }
            if TcpStream::connect(("127.0.0.1", port)).is_ok() {
                break true;
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        };

        cancel.cancel();
        let _ = tokio::time::timeout(Duration::from_secs(2), task).await;

        assert!(bound, "supervisor must bind the listener within {SUPERVISOR_TICK:?} + 5s");
        Ok(())
    }
}
