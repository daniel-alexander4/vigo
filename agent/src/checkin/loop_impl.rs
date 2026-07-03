// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Daemon loop with polling + streaming modes.
//!
//! Mirrors the Go `Loop()`, `streamLoop()`, `converge()`, `RunOnce()` functions.

use crate::checkin::backoff::Backoff;
use crate::checkin::client::Client;
use crate::checkin::configcratecache::{merge_stubs, ConfigcrateCache};
use crate::checkin::stream;
use crate::checkin::verify;
use crate::customtraits;
use crate::executors::{Action, Executor, ResourceResult};
use crate::proto::{
    agent_stream_message, delta_event, AgentStreamMessage, CheckInResponse, CustomTraitEntry,
    DeltaEvent, HeartbeatDelta, ResourceRunResult, PolicyBundle, ProxiedDevice, RunCompleted,
    StreamCapabilities,
};
use crate::runner::{self, cache::ResultCache, RunOptions};
use crate::snapshots;
use crate::state::StateStore;
use crate::traits::cached::CachedCollectors;
use anyhow::Result;
use rand::Rng;
use serde_json::Value;
use std::collections::HashMap;
use std::path::Path;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tokio::time::Instant;
use tracing::{debug, info, warn};

use crate::swarm::peermap::PeerMap;

/// Reason the stream loop exited.
enum StreamExit {
    /// Server sent StreamRelease — graceful close, no backoff.
    Released,
    /// Stream error or server closed the connection.
    Disconnected(Option<anyhow::Error>),
    /// Shutdown signal received.
    Cancelled,
}

/// Configuration for the agent self-update mechanism.
pub struct UpdateConfig {
    pub api_addr: String,
    pub current_version: String,
    pub tls_certs: Option<crate::update::TlsCerts>,
}

/// Configuration for the daemon loop.
pub struct LoopConfig {
    pub trait_refresh_cycles: usize,
    pub result_cache_cycles: usize,
    pub traits_cache_dir: Option<String>,
    pub update_cfg: Option<UpdateConfig>,
    /// Directory for LMDB state store (enables offline convergence).
    /// When None, the agent behaves exactly as before — no persistent state.
    pub state_store: Option<Arc<StateStore>>,
    /// Server's bundle-signing public key PEM (empty = no verification).
    pub server_pubkey_pem: String,
    /// Agent data directory for persisting server pubkey updates.
    pub data_dir: Option<std::path::PathBuf>,
    /// Shared peer map for swarm P2P downloads (None = server-only fallback).
    pub peer_map: Option<Arc<PeerMap>>,
    /// Shared swarm manifest for decentralized blob lifecycle.
    pub manifest: Option<Arc<crate::swarm::manifest::Manifest>>,
    /// Swarm download coordinator. When set, check-in responses are forwarded
    /// to the manager instead of spawning ad-hoc download tasks.
    pub swarm_manager: Option<crate::swarm::manager::SwarmManager>,
    /// Live on/off switch for the gitback subsystem. Re-evaluated after
    /// every accepted policy bundle so operators can flip `gitback: true`
    /// in a hostcrate without restarting the agent.
    pub gitback_gate: Option<crate::swarm::gitback::Gate>,
}

impl Default for LoopConfig {
    fn default() -> Self {
        Self {
            trait_refresh_cycles: 10,
            result_cache_cycles: 5,
            traits_cache_dir: None,
            update_cfg: None,
            state_store: None,
            server_pubkey_pem: String::new(),
            data_dir: None,
            peer_map: None,
            manifest: None,
            swarm_manager: None,
            gitback_gate: None,
        }
    }
}

/// Result of a single convergence run.
pub struct RunResult {
    pub results: Vec<ResourceResult>,
    pub interval: Duration,
    pub jitter_pct: i32,
    pub no_change: bool,
    pub response_fingerprint: String,
    pub policy_version: u64,
    pub policy_bundle: Option<PolicyBundle>,
    pub stream_requested: bool,
    pub tunnel_requested: bool,
    /// Network devices this gateway should converge (empty for non-gateways).
    pub proxied_devices: Vec<ProxiedDevice>,
    /// Server's bundle-signing public key PEM (updated on each check-in).
    pub server_pubkey_pem: String,
    /// Server requests immediate binary update and restart.
    pub force_update: bool,
    /// Manifest entries from the server (fallback gossip channel).
    pub swarm_manifest_entries: Vec<crate::proto::SwarmManifestEntry>,
    /// Curator fleet catalog (ADR-024): JSON `[]CatalogEntry` the server
    /// attaches to every check-in; cached agent-side so resolution survives a
    /// server outage. Empty when the server hasn't published one yet.
    pub curator_catalog: Vec<u8>,
    /// Bundle's ED25519 signature failed verification. Set inside
    /// `run_once_with_update` before any mutation of the bundle; the
    /// daemon loop reads this and sleeps the response's interval before
    /// the next check-in rather than treating the result as a successful
    /// cycle. Required because `secrets::resolve_and_substitute` mutates
    /// signed fields (vars_json byte representation, module/resource
    /// `when:` strings, attribute values) — verifying after substitution
    /// is unsound, so verify happens before substitute and the rejection
    /// is signaled back here.
    pub bundle_verify_failed: bool,
}

/// Accepted policy state across convergence cycles.
pub struct PolicyState {
    pub version: u64,
    pub accepted_bundle: Option<PolicyBundle>,
    pub module_cache: ConfigcrateCache,
    pub custom_traits: Vec<CustomTraitEntry>,
    /// When the current bundle was accepted (Unix seconds).
    pub accepted_at: u64,
}

impl PolicyState {
    pub fn new() -> Self {
        Self {
            version: 0,
            accepted_bundle: None,
            module_cache: ConfigcrateCache::new(),
            custom_traits: Vec::new(),
            accepted_at: 0,
        }
    }

    /// Age of the accepted bundle in seconds (0 if no bundle).
    pub fn bundle_age_secs(&self) -> u64 {
        if self.accepted_at == 0 {
            return 0;
        }
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        now.saturating_sub(self.accepted_at)
    }

    /// Check if the accepted bundle has expired based on max_age_seconds.
    /// Returns false if max_age is 0 (no expiry) or no bundle is accepted.
    pub fn is_bundle_expired(&self) -> bool {
        let bundle = match &self.accepted_bundle {
            Some(b) => b,
            None => return false,
        };
        let max_age = bundle.max_age_seconds;
        if max_age == 0 || self.accepted_at == 0 {
            return false;
        }
        self.bundle_age_secs() > max_age as u64
    }

    /// Check if the bundle is approaching expiry (>80% of max_age).
    /// Returns false if max_age is 0 or no bundle.
    pub fn needs_renewal(&self) -> bool {
        let bundle = match &self.accepted_bundle {
            Some(b) => b,
            None => return false,
        };
        let max_age = bundle.max_age_seconds;
        if max_age == 0 || self.accepted_at == 0 {
            return false;
        }
        self.bundle_age_secs() > (max_age as u64 * 4 / 5)
    }
}

/// Add random jitter to a base duration: base ± (jitter_pct% of base).
fn jittered_duration(base: Duration, jitter_pct: i32) -> Duration {
    if jitter_pct <= 0 {
        return base;
    }
    let jitter = rand::thread_rng().gen_range(-1.0..1.0)
        * base.as_secs_f64()
        * (jitter_pct as f64 / 100.0);
    let secs = base.as_secs_f64() + jitter;
    Duration::from_secs_f64(secs.max(0.1))
}

/// Call the Wake RPC instead of sleeping. The server holds the response
/// until urgent work arrives or the timeout expires. Falls back to normal
/// sleep if the RPC fails (e.g., server doesn't support Wake yet).
async fn wake_or_sleep(client: &mut Client, timeout: Duration) {
    match client.wake(timeout).await {
        Ok(resp) if !resp.reason.is_empty() => {
            info!(reason = %resp.reason, "server woke agent");
        }
        Ok(_) => {
            // Timeout — normal, proceed to next check-in.
        }
        Err(e) => {
            // Wake RPC unavailable — fall back to normal sleep.
            debug!(error = %e, "wake RPC unavailable, sleeping normally");
            tokio::time::sleep(timeout).await;
        }
    }
}

/// Convert agent ResourceResults to protobuf ResourceRunResults.
fn result_to_proto(r: &ResourceResult) -> ResourceRunResult {
    ResourceRunResult {
        configcrate_name: r.configcrate_name.clone(),
        resource_type: r.resource_type.clone(),
        resource_name: r.resource_name.clone(),
        action_taken: r.action.to_string(),
        changed: r.changed,
        error_message: r.error.clone().unwrap_or_default(),
        duration_ms: r.duration_ms,
        content_hash: r.content_hash.clone().unwrap_or_default(),
        file_path: r.file_path.clone().unwrap_or_default(),
    }
}

/// reportable_results prepares a unary ReportResult payload: it drops settled
/// no-ops (unchanged, no error, Action::None) and returns (rows, total_count).
/// The server records `total_count` as the run's resource total and folds the
/// elided no-ops into skipped, so only the changed/failed/blocked rows cross
/// the wire and hit the run_results table — the dominant steady-state upload on
/// the poll path. Blocked resources (Action::Blocked) are retained, unlike the
/// stream delta filter.
fn reportable_results(results: &[ResourceResult]) -> (Vec<ResourceRunResult>, i32) {
    let total = results.len() as i32;
    let rows = results
        .iter()
        .filter(|r| r.changed || r.error.is_some() || r.action != Action::None)
        .map(result_to_proto)
        .collect();
    (rows, total)
}

/// Strip _secret_triggered from cached bundle to prevent re-firing on
/// subsequent no-change cycles.
fn strip_secret_triggers(bundle: &mut PolicyBundle) {
    for m in &mut bundle.configcrates {
        if m.stub {
            continue;
        }
        for r in &mut m.resources {
            r.attributes.remove("_secret_triggered");
        }
    }
}

/// Check a CheckInResponse for a reassignment directive.
/// Returns an error (causing the loop to restart) if reassignment is pending.
fn check_reassignment(resp: &CheckInResponse) -> Result<()> {
    if !resp.reassign_addr.is_empty() {
        info!(
            target_addr = %resp.reassign_addr,
            new_id = %resp.reassign_id,
            "reassignment received, agent should reconnect to new bolt"
        );
        anyhow::bail!(
            "reassignment to {} with new ID {}",
            resp.reassign_addr,
            resp.reassign_id
        );
    }
    Ok(())
}

/// Perform a convergence cycle with an already-received CheckInResponse.
///
/// Used by both RunOnce (poll) and the stream event loop.
/// Acquires a file lock to prevent concurrent convergence (e.g. daemon + manual run).
fn converge(
    resp: &CheckInResponse,
    executors: &HashMap<String, Box<dyn Executor>>,
    traits_val: &Value,
    cache: &mut ResultCache,
    policy_state: &mut PolicyState,
    dry_run: bool,
    module_filter: Option<&str>,
    data_dir: &std::path::Path,
    state_store: Option<&StateStore>,
    pkg_breaker: &mut runner::pkg_breaker::PackageBreaker,
) -> Result<RunResult> {
    for w in &resp.warnings {
        warn!("server: {}", w);
    }

    // Parse server-provided timing.
    let interval = if resp.interval.is_empty() {
        Duration::from_secs(300) // 5m default
    } else {
        parse_go_duration(&resp.interval).unwrap_or(Duration::from_secs(300))
    };
    let jitter_pct = if resp.jitter_percent == 0 {
        10 // default
    } else {
        resp.jitter_percent
    };

    let proxied_devices = resp.proxied_devices.clone();

    // Determine which policy bundle to evaluate.
    let mut bundle = if resp.no_change && policy_state.accepted_bundle.is_some() {
        // Config unchanged — evaluate cached bundle with fresh traits.
        policy_state.accepted_bundle.clone().unwrap()
    } else if let Some(ref b) = resp.policy_bundle {
        b.clone()
    } else if resp.no_change {
        // First check-in with no cached bundle and server said no_change.
        return Ok(RunResult {
            results: vec![],
            interval,
            jitter_pct,
            no_change: true,
            response_fingerprint: String::new(),
            policy_version: resp.policy_version,
            policy_bundle: None,
            stream_requested: resp.stream_requested,
            tunnel_requested: resp.tunnel_requested,
            proxied_devices,
            server_pubkey_pem: resp.server_pubkey_pem.clone(),
            force_update: resp.force_update,
            swarm_manifest_entries: resp.manifest_entries.clone(),
            curator_catalog: resp.curator_catalog.clone(),
            bundle_verify_failed: false,
        });
    } else {
        return Ok(RunResult {
            results: vec![],
            interval,
            jitter_pct,
            no_change: false,
            response_fingerprint: resp.response_fingerprint.clone(),
            policy_version: resp.policy_version,
            policy_bundle: None,
            stream_requested: resp.stream_requested,
            tunnel_requested: resp.tunnel_requested,
            proxied_devices,
            server_pubkey_pem: resp.server_pubkey_pem.clone(),
            force_update: resp.force_update,
            swarm_manifest_entries: resp.manifest_entries.clone(),
            curator_catalog: resp.curator_catalog.clone(),
            bundle_verify_failed: false,
        });
    };

    // Delta transfer: fill stub modules from cache.
    bundle.configcrates = merge_stubs(bundle.configcrates, &policy_state.module_cache);

    // Update module cache with fully resolved modules.
    policy_state.module_cache.update(&bundle.configcrates);

    // Evaluate the policy bundle with traits.
    let modules = match crate::policy::eval::eval_bundle(Some(&bundle), traits_val) {
        Ok(m) => m,
        Err(e) => {
            warn!("policy evaluation failed, skipping run: {}", e);
            return Ok(RunResult {
                results: vec![],
                interval,
                jitter_pct,
                no_change: false,
                response_fingerprint: resp.response_fingerprint.clone(),
                policy_version: resp.policy_version,
                policy_bundle: resp.policy_bundle.clone(),
                stream_requested: resp.stream_requested,
                tunnel_requested: resp.tunnel_requested,
                proxied_devices: proxied_devices.clone(),
                server_pubkey_pem: resp.server_pubkey_pem.clone(),
                force_update: resp.force_update,
                swarm_manifest_entries: resp.manifest_entries.clone(),
                curator_catalog: resp.curator_catalog.clone(),
                bundle_verify_failed: false,
            });
        }
    };

    // Strip one-shot _secret_triggered flags. (This cycle's runner still sees
    // them via `modules` above, evaluated before the strip.) `update_state`
    // applies the same merge_stubs + strip_secret_triggers to the bundle it
    // persists as `accepted_bundle` — keep the two in sync.
    strip_secret_triggers(&mut bundle);

    if modules.is_empty() {
        warn!("server returned zero modules — check server logs for config errors (missing modules/roles) and verify this envoy is matched in envoys/");
        return Ok(RunResult {
            results: vec![],
            interval,
            jitter_pct,
            no_change: false,
            response_fingerprint: resp.response_fingerprint.clone(),
            policy_version: resp.policy_version,
            policy_bundle: resp.policy_bundle.clone(),
            stream_requested: resp.stream_requested,
            tunnel_requested: resp.tunnel_requested,
            proxied_devices,
            server_pubkey_pem: resp.server_pubkey_pem.clone(),
            force_update: resp.force_update,
            swarm_manifest_entries: resp.manifest_entries.clone(),
            curator_catalog: resp.curator_catalog.clone(),
            bundle_verify_failed: false,
        });
    }

    // Acquire convergence lock — prevents daemon + manual run collisions.
    let _lock = match runner::lock::ConvergenceLock::try_acquire(data_dir) {
        Ok(Some(guard)) => Some(guard),
        Ok(None) => {
            warn!("skipping convergence — another process holds the lock");
            return Ok(RunResult {
                results: vec![],
                interval,
                jitter_pct,
                no_change: true,
                response_fingerprint: String::new(),
                policy_version: resp.policy_version,
                policy_bundle: policy_state.accepted_bundle.clone(),
                stream_requested: resp.stream_requested,
                tunnel_requested: resp.tunnel_requested,
                proxied_devices,
                server_pubkey_pem: resp.server_pubkey_pem.clone(),
                force_update: resp.force_update,
                swarm_manifest_entries: resp.manifest_entries.clone(),
                curator_catalog: resp.curator_catalog.clone(),
                bundle_verify_failed: false,
            });
        }
        Err(e) => {
            warn!(error = %e, "failed to acquire convergence lock, proceeding unlocked");
            None
        }
    };

    // Observe-only mode: server sets resp.observe_only to force dry-run (report drift only).
    let observe_only = resp.observe_only;
    if observe_only && !dry_run {
        info!("observe-only mode active — reporting drift only, no changes will be applied");
    }

    let run_id = uuid::Uuid::new_v4().to_string();
    let opts = RunOptions {
        dry_run: dry_run || observe_only,
        module_filter: module_filter.map(|s| s.to_string()),
        run_id,
        stream_edit_allowed_paths: bundle.stream_edit_allowed_paths.clone(),
        stream_edit_timeout_secs: bundle.stream_edit_timeout_secs,
        data_dir: Some(data_dir.to_path_buf()),
    };

    let results = runner::converge(&modules, executors, cache, &opts, traits_val, pkg_breaker);

    // Cache hygiene: drop checksum entries for paths no longer in policy.
    // Throttled to ~60s inside the helper; no-op on dry/observe runs is OK
    // because the active-paths set still reflects current policy intent.
    runner::try_prune_stale_checksums(state_store, &modules);

    Ok(RunResult {
        results,
        interval,
        jitter_pct,
        no_change: false,
        response_fingerprint: resp.response_fingerprint.clone(),
        policy_version: resp.policy_version,
        policy_bundle: resp.policy_bundle.clone(),
        stream_requested: resp.stream_requested,
        tunnel_requested: resp.tunnel_requested,
        proxied_devices,
        server_pubkey_pem: resp.server_pubkey_pem.clone(),
        force_update: resp.force_update,
        swarm_manifest_entries: resp.manifest_entries.clone(),
        curator_catalog: resp.curator_catalog.clone(),
        bundle_verify_failed: false,
    })
}

/// Perform a single check-in, evaluate, execute, and report.
pub async fn run_once(
    client: &mut Client,
    executors: &HashMap<String, Box<dyn Executor>>,
    traits_val: &Value,
    traits_json: Option<Vec<u8>>,
    cache: &mut ResultCache,
    policy_state: &mut PolicyState,
    dry_run: bool,
    module_filter: Option<&str>,
    state_fingerprint: &str,
    data_dir: &std::path::Path,
    pkg_breaker: &mut runner::pkg_breaker::PackageBreaker,
) -> Result<RunResult> {
    run_once_with_update(
        client, executors, traits_val, traits_json, cache, policy_state,
        dry_run, module_filter, state_fingerprint, data_dir, None, None, None, None, pkg_breaker,
        "",
    ).await
}

/// Perform a single check-in cycle with optional pre-convergence self-update.
/// When update_cfg is provided and the server sets force_update, the agent
/// downloads the new binary and exec()s BEFORE convergence starts.
///
/// `server_pubkey_pem` is the agent's cached bundle-signing pubkey. The
/// signature on `resp.policy_bundle` is verified against either it or
/// `resp.server_pubkey_pem` (which the server includes on every response
/// — preferred when present, falling back to the cache for the empty
/// case). Verification must happen BEFORE
/// [`crate::policy::secrets::resolve_and_substitute`] mutates signed
/// fields (vars_json byte representation, module/resource `when:`
/// strings, attribute values); verifying afterward would check bytes
/// the server never signed.
pub async fn run_once_with_update(
    client: &mut Client,
    executors: &HashMap<String, Box<dyn Executor>>,
    traits_val: &Value,
    traits_json: Option<Vec<u8>>,
    cache: &mut ResultCache,
    policy_state: &mut PolicyState,
    dry_run: bool,
    module_filter: Option<&str>,
    state_fingerprint: &str,
    data_dir: &std::path::Path,
    update_cfg: Option<&UpdateConfig>,
    _peer_map: Option<&Arc<PeerMap>>,
    _manifest: Option<&Arc<crate::swarm::manifest::Manifest>>,
    state_store: Option<&StateStore>,
    pkg_breaker: &mut runner::pkg_breaker::PackageBreaker,
    server_pubkey_pem: &str,
) -> Result<RunResult> {
    let mut resp = client
        .check_in(
            state_fingerprint,
            traits_json,
            policy_state.version,
            policy_state.module_cache.hashes(),
        )
        .await?;

    // Verify the bundle signature BEFORE any mutation. Substitute touches
    // signed fields; verifying after substitution checks bytes the server
    // never signed. Use the response's pubkey when the server included one
    // (handles in-band key rotation), else fall back to the cached pubkey
    // the caller threaded in. On failure, return early with
    // `bundle_verify_failed: true` so the daemon loop sleeps the response's
    // interval and tries the next check-in — never accepts a bundle whose
    // signature didn't match.
    if let Some(ref bundle) = resp.policy_bundle {
        let pubkey_for_verify = if !resp.server_pubkey_pem.is_empty() {
            resp.server_pubkey_pem.as_str()
        } else {
            server_pubkey_pem
        };
        if !pubkey_for_verify.is_empty() {
            if let Err(e) = verify::verify_bundle(bundle, pubkey_for_verify) {
                warn!(error = %e, "bundle signature verification failed, rejecting");
                let interval = if resp.interval.is_empty() {
                    Duration::from_secs(300)
                } else {
                    parse_go_duration(&resp.interval).unwrap_or(Duration::from_secs(300))
                };
                return Ok(RunResult {
                    results: vec![],
                    interval,
                    jitter_pct: resp.jitter_percent,
                    no_change: false,
                    response_fingerprint: resp.response_fingerprint.clone(),
                    policy_version: resp.policy_version,
                    policy_bundle: None,
                    stream_requested: false,
                    tunnel_requested: false,
                    proxied_devices: vec![],
                    server_pubkey_pem: resp.server_pubkey_pem.clone(),
                    force_update: false,
                    swarm_manifest_entries: vec![],
                    curator_catalog: vec![],
                    bundle_verify_failed: true,
                });
            }
        }
    }

    // Resolve placeholder secrets in-place before eval_bundle sees the
    // strings. Walks vars_json + every module's resource attributes + the
    // module/resource `when:` strings for `__VIGO_SECRET__<key>__END_VIGO_SECRET__`
    // markers, fetches missing values via ResolveSecrets, substitutes.
    // Failure here aborts the apply rather than letting a placeholder
    // string flow through to an executor — see `secrets::substitute_in_bundle`.
    if let Some(bundle) = resp.policy_bundle.as_mut() {
        if let Err(e) = crate::policy::secrets::resolve_and_substitute(
            bundle,
            crate::policy::secrets::global_cache(),
            client,
        )
        .await
        {
            warn!("secret resolution/substitution failed, skipping convergence: {}", e);
            return Ok(RunResult {
                results: vec![],
                interval: Duration::ZERO,
                jitter_pct: resp.jitter_percent,
                no_change: false,
                response_fingerprint: resp.response_fingerprint.clone(),
                policy_version: resp.policy_version,
                policy_bundle: resp.policy_bundle.clone(),
                stream_requested: resp.stream_requested,
                tunnel_requested: resp.tunnel_requested,
                proxied_devices: resp.proxied_devices.clone(),
                server_pubkey_pem: resp.server_pubkey_pem.clone(),
                force_update: resp.force_update,
                swarm_manifest_entries: resp.manifest_entries.clone(),
                curator_catalog: resp.curator_catalog.clone(),
                bundle_verify_failed: false,
            });
        }
    }

    debug!(
        force_update = resp.force_update,
        no_change = resp.no_change,
        policy_version = resp.policy_version,
        manifest_entries = resp.manifest_entries.len(),
        "check-in response received"
    );

    // Refresh the swarm runtime-limits cache from this response if the
    // server populated the field. Done unconditionally on every CheckIn
    // (not just no-change cycles) so admin tweaks to server.yaml swarm
    // caps reach agents within one cycle. Read sites in lockbox /
    // gitback / longdrawer consult crate::swarm::runtime_limits.
    if let Some(ref limits) = resp.swarm_runtime_limits {
        crate::swarm::runtime_limits::update_from_proto(limits);
    }

    // Scrier tunnel: spawn IMMEDIATELY on a separate connection/task.
    // Tunnel requests are time-sensitive (30s server timeout) and must not
    // wait for self-update or convergence. The relay runs independently.
    if resp.tunnel_requested {
        info!("server requested tunnel stream (Scrier), spawning before convergence");
        match client.new_tunnel_client().await {
            Ok(tunnel_client) => {
                let envoy_id = client.envoy_id().to_string();
                let (sig, ts) = client.sign(envoy_id.as_bytes());
                tokio::spawn(async move {
                    if let Err(e) = crate::tunnel::relay::run(tunnel_client, &envoy_id, sig, ts).await {
                        warn!(error = %e, "tunnel relay failed");
                    }
                });
            }
            Err(e) => {
                warn!(error = %e, "failed to create tunnel gRPC client for Scrier");
            }
        }
    }

    // Swarm downloads are handled by the caller (daemon_loop / stream_loop)
    // via the SwarmManager. run_once_with_update only returns the assignments
    // in RunResult for the caller to forward.

    // Self-update BEFORE convergence — the new binary may contain fixes
    // that affect convergence itself. exec() replaces the process immediately.
    if resp.force_update {
        if let Some(ucfg) = update_cfg {
            if !ucfg.api_addr.is_empty() {
                info!("server requested forced update, downloading before convergence");
                crate::update::force_self_update(&ucfg.api_addr, &ucfg.current_version, ucfg.tls_certs.as_ref()).await;
                // If we get here, exec() failed or update wasn't available.
                // Continue with convergence on current binary.
            }
        }
    }

    // Check for reassignment before converging.
    if !resp.reassign_addr.is_empty() {
        info!(
            target_addr = %resp.reassign_addr,
            new_id = %resp.reassign_id,
            "reassignment received, agent should reconnect to new bolt"
        );
        anyhow::bail!(
            "reassignment to {} with new ID {}",
            resp.reassign_addr,
            resp.reassign_id
        );
    }

    let rr = converge(
        &resp, executors, traits_val, cache, policy_state, dry_run, module_filter, data_dir, state_store, pkg_breaker,
    )?;

    // Report results back to server (skip in dry-run mode).
    if !dry_run && !rr.results.is_empty() {
        let run_id = uuid::Uuid::new_v4().to_string();
        let (proto_results, total) = reportable_results(&rr.results);
        if let Err(e) = client.report_result(&run_id, proto_results, total).await {
            warn!(error = %e, "failed to report results");
        }
    }

    Ok(rr)
}

/// Update policy state from a run result. When a new bundle is accepted,
/// records the acceptance time for TTL tracking.
fn update_state(
    ps: &mut PolicyState,
    rr: &RunResult,
    last_fingerprint: Option<&mut String>,
) {
    if let Some(fp) = last_fingerprint {
        if !rr.response_fingerprint.is_empty() {
            *fp = rr.response_fingerprint.clone();
        }
    }
    if rr.policy_version > 0 {
        ps.version = rr.policy_version;
    }
    if let Some(ref wire) = rr.policy_bundle {
        if !wire.custom_traits.is_empty() {
            ps.custom_traits = wire.custom_traits.clone();
        }
        // `rr.policy_bundle` is the bundle exactly as the server transmitted it
        // — that form is what `verify_bundle` checks the signature against, so
        // it must not be altered before verification. But after the first
        // check-in its modules are delta-transfer *stubs* (name + content hash,
        // no resources); persisting it as-is leaves the cached bundle with zero
        // resources, which is what offline `vigo run` and the no_change fast
        // path re-use. Store the *resolved* form instead: stubs filled from the
        // module cache (already updated with this cycle's resolved modules in
        // `converge`), one-shot secret triggers stripped. Keep these two
        // transforms in sync with `converge`'s `merge_stubs` + `strip_secret_triggers`.
        let mut resolved = wire.clone();
        resolved.configcrates = merge_stubs(resolved.configcrates, &ps.module_cache);
        strip_secret_triggers(&mut resolved);
        ps.accepted_bundle = Some(resolved);
        ps.accepted_at = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
    }
}

/// Log a convergence run summary.
fn log_run_result(rr: &RunResult) {
    if rr.no_change {
        info!("no change detected, skipping execution");
        return;
    }
    let changed = rr.results.iter().filter(|r| r.changed).count();
    let errors = rr.results.iter().filter(|r| r.error.is_some()).count();
    info!(
        total = rr.results.len(),
        changed = changed,
        errors = errors,
        "convergence run complete"
    );
}

/// Collect traits via cached collectors and serialize to JSON.
/// If custom traits are configured, syncs and merges them into the result.
async fn collect_traits(
    collectors: &CachedCollectors,
    client: &mut Client,
    custom_trait_entries: &[CustomTraitEntry],
    traits_cache_dir: Option<&str>,
) -> (Value, Option<Vec<u8>>) {
    let mut traits_val = collectors.collect_all().await;

    // Sync and run custom traits if configured.
    if let Some(cache_dir) = traits_cache_dir {
        if !custom_trait_entries.is_empty() {
            let manifest = customtraits::manifest_from_proto(custom_trait_entries);
            let exec = customtraits::Executor::new(
                Path::new(cache_dir),
                Duration::from_secs(10),
            );

            // Client implements Downloader via the adapter below.
            let mut adapter = ClientDownloader(client);
            match exec.sync_and_run(&mut adapter, &manifest).await {
                Ok(custom_val) => {
                    // Merge custom traits into the main traits map.
                    if let (Some(main_map), Some(custom_map)) =
                        (traits_val.as_object_mut(), custom_val.as_object())
                    {
                        for (k, v) in custom_map {
                            main_map.insert(k.clone(), v.clone());
                        }
                    }
                }
                Err(e) => {
                    warn!(error = %e, "custom traits sync/run failed");
                }
            }
        }
    }

    let traits_json = serde_json::to_vec(&traits_val).ok();
    (traits_val, traits_json)
}

/// Collect traits without custom trait sync (for stream mode where we
/// don't have mutable client access — custom traits are synced at poll time).
async fn collect_traits_basic(collectors: &CachedCollectors) -> (Value, Option<Vec<u8>>) {
    let traits_val = collectors.collect_all().await;
    let traits_json = serde_json::to_vec(&traits_val).ok();
    (traits_val, traits_json)
}

/// Adapter to implement `customtraits::Downloader` for `Client`.
struct ClientDownloader<'a>(&'a mut Client);

#[async_trait::async_trait]
impl<'a> customtraits::Downloader for ClientDownloader<'a> {
    async fn download_custom_trait(&mut self, name: &str) -> Result<Vec<u8>> {
        self.0.download_custom_trait(name).await
    }
}

/// Deliver any pending delta events from offline convergence.
/// Called on reconnect (both stream and polling modes).
async fn deliver_pending(client: &mut Client, store: &StateStore) {
    // Snapshot the queue read-only — no transaction is held across the
    // delivery RPCs, and only the entries we deliver successfully are removed
    // afterward. A failed report_result leaves its entry queued for the next
    // reconnect (the queue exists precisely to survive this); and since the
    // server keys runs by its own id, re-delivering a succeeded entry would
    // duplicate it — so delete-on-success-only is the rule.
    let (entries, corrupt) = {
        let rtxn = match store.read_txn() {
            Ok(t) => t,
            Err(e) => {
                warn!(error = %e, "failed to open read txn for pending delivery");
                return;
            }
        };
        match store.pending.peek(&rtxn) {
            Ok(v) => v,
            Err(e) => {
                warn!(error = %e, "failed to read pending results");
                return;
            }
        }
    };

    if entries.is_empty() && corrupt.is_empty() {
        return;
    }
    if !entries.is_empty() {
        info!(count = entries.len(), "delivering pending offline results");
    }

    // Corrupt entries are undeliverable garbage — purge them regardless.
    let mut to_remove = corrupt;
    for entry in &entries {
        match &entry.delta.event {
            Some(delta_event::Event::Run(run)) => {
                match client
                    .report_result(&run.run_id, run.results.clone(), run.total_count)
                    .await
                {
                    Ok(_) => to_remove.push(entry.key.clone()),
                    Err(e) => warn!(
                        run_id = %run.run_id,
                        error = %e,
                        "failed to deliver pending result; leaving queued for next reconnect"
                    ),
                }
            }
            // Non-Run deltas (e.g. heartbeats) carry no run results to deliver;
            // drop them rather than re-peek them every reconnect.
            _ => to_remove.push(entry.key.clone()),
        }
    }

    if to_remove.is_empty() {
        return; // nothing delivered (server unreachable) — retry next reconnect.
    }
    let mut wtxn = match store.write_txn() {
        Ok(t) => t,
        Err(e) => {
            warn!(error = %e, "failed to open write txn to clear delivered results");
            return;
        }
    };
    if let Err(e) = store.pending.remove(&mut wtxn, &to_remove) {
        warn!(error = %e, "failed to remove delivered pending results");
        return;
    }
    if let Err(e) = wtxn.commit() {
        warn!(error = %e, "failed to commit pending removal");
    }
}

/// Re-read the gitback authorization vars from the freshly-accepted
/// bundle and propagate the result to the shared gate. Logs only on
/// transitions so the steady state is quiet.
fn sync_gitback_gate(
    gate: Option<&crate::swarm::gitback::Gate>,
    store: &StateStore,
) {
    let Some(gate) = gate else { return };
    let new_state = crate::swarm::gitback::gitback_enabled_for_envoy(Some(store));
    let prev = gate.set(new_state);
    if prev != new_state {
        if new_state {
            info!("gitback: enabled (this envoy now matches swarm.gitback.enabled; live flip, no restart required)");
        } else {
            info!("gitback: disabled (this envoy no longer matches swarm.gitback.enabled; live flip, reactor will idle)");
        }
    }
}

/// Accept a policy bundle: persist to LMDB and cache trait scripts.
/// The agent owns this bundle once accepted — it is the source of truth
/// for convergence until a newer bundle replaces it.
fn accept_bundle(store: &StateStore, policy_state: &PolicyState, traits_json: Option<&[u8]>) {
    if let Some(ref bundle) = policy_state.accepted_bundle {
        if let Ok(mut wtxn) = store.write_txn() {
            if let Err(e) = store.policy.store(&mut wtxn, policy_state.version, bundle) {
                warn!(error = %e, "failed to persist accepted bundle");
            }
            if let Some(tj) = traits_json {
                if let Err(e) = store.traits.store(&mut wtxn, tj) {
                    warn!(error = %e, "failed to persist traits");
                }
            }
            // Persist custom trait script entries for offline re-collection.
            if !bundle.custom_traits.is_empty() {
                if let Err(e) = store.traits.store_scripts(&mut wtxn, &bundle.custom_traits) {
                    warn!(error = %e, "failed to persist trait scripts");
                }
            }
            let _ = wtxn.commit();
        }
        // Mirror the resolved swarm gate state to the user-readable
        // /var/lib/vigo/swarm/state.json (0.41.1+) so `vigo swarm
        // status` works without sudo. Best-effort — a failure here
        // never blocks bundle acceptance.
        let path = crate::swarm::state_mirror::default_path();
        if let Err(e) = crate::swarm::state_mirror::write(
            &path,
            env!("CARGO_PKG_VERSION"),
            policy_state.version,
            &bundle.vars_json,
        ) {
            warn!(error = %format!("{e:#}"), path = %path.display(), "failed to write swarm state mirror");
        }
    }
}

/// Converge locally using accepted policy bundle and cached traits.
/// This is normal autonomous operation, not a degraded mode.
/// Returns results to be enqueued for delivery when the server is reachable.
fn converge_local(
    store: &StateStore,
    executors: &HashMap<String, Box<dyn Executor>>,
    result_cache: &mut ResultCache,
    policy_state: &mut PolicyState,
    data_dir: &std::path::Path,
    pkg_breaker: &mut runner::pkg_breaker::PackageBreaker,
) -> Option<(RunResult, Vec<u8>)> {
    let rtxn = store.read_txn().ok()?;

    // Load accepted policy bundle.
    let (version, bundle) = store.policy.load(&rtxn).ok()??;

    // Check bundle expiry before converging.
    if bundle.max_age_seconds > 0 && policy_state.accepted_at > 0 {
        let age = policy_state.bundle_age_secs();
        if age > bundle.max_age_seconds as u64 {
            warn!(
                version = version,
                age_secs = age,
                max_age_secs = bundle.max_age_seconds,
                "bundle expired, skipping convergence until refreshed"
            );
            return None;
        }
    }

    // Load cached traits (any age — local convergence uses what's available).
    let traits_bytes = store.traits.load_any(&rtxn).ok()??;
    drop(rtxn);

    let traits_val: Value = serde_json::from_slice(&traits_bytes).ok()?;

    let age = policy_state.bundle_age_secs();
    info!(
        version = version,
        accepted_ago_secs = age,
        "converging bundle v{version} (accepted {age}s ago)"
    );

    // Offline-cache limitation (v1): the resolved-secrets cache is
    // process-lifetime in-memory, so a fresh agent process that hasn't
    // checked in yet has nothing to substitute placeholders against.
    // Refuse to apply a placeholder-bearing bundle from the local store
    // — silently writing `__VIGO_SECRET__…__END_VIGO_SECRET__` into a
    // config file is the catastrophic failure mode we built this whole
    // pipeline to prevent. The next successful check-in re-populates
    // the cache and the next online converge() path applies normally.
    let refs = crate::policy::secrets::collect_refs_in_bundle_pub(&bundle);
    let cache = crate::policy::secrets::global_cache();
    let missing = cache.missing_refs_pub(refs.iter().map(|s| s.as_str()));
    if !missing.is_empty() {
        warn!(
            version = version,
            missing_count = missing.len(),
            "offline reconvergence: {} placeholder secrets not in in-memory cache, skipping until next online check-in",
            missing.len()
        );
        return None;
    }
    let mut bundle = bundle.clone();
    if let Err(e) = crate::policy::secrets::substitute_in_bundle_pub(&mut bundle, cache) {
        warn!(error = %e, "offline reconvergence: secret substitution failed, skipping");
        return None;
    }

    // Build a synthetic response from accepted bundle (now substituted).
    let synth_resp = CheckInResponse {
        policy_bundle: Some(bundle.clone()),
        policy_version: version,
        ..Default::default()
    };

    match converge(
        &synth_resp,
        executors,
        &traits_val,
        result_cache,
        policy_state,
        false,
        None,
        data_dir,
        Some(store),
        pkg_breaker,
    ) {
        Ok(rr) => {
            // Update policy state so subsequent cycles use this bundle.
            policy_state.version = version;
            policy_state.accepted_bundle = Some(bundle);
            Some((rr, traits_bytes))
        }
        Err(e) => {
            warn!(error = %e, "local convergence failed");
            None
        }
    }
}

/// Run the daemon loop until the cancellation token fires.
///
/// Defaults to unary polling. When the server sets `stream_requested` in a
/// CheckInResponse, the agent opens a bidirectional stream to receive dispatched
/// work (tasks, queries). Once work is complete and the server sends
/// `StreamRelease`, the agent closes the stream and resumes polling.
///
/// When the server is completely unreachable and a state store is available,
/// converges offline using cached policy and queues results for later delivery.
pub async fn daemon_loop(
    cancel: tokio_util::sync::CancellationToken,
    client: &mut Client,
    executors: &HashMap<String, Box<dyn Executor>>,
    _snapshot_mgr: Option<&snapshots::Manager>,
    cfg: LoopConfig,
) -> Result<()> {
    let trait_refresh = if cfg.trait_refresh_cycles == 0 {
        10
    } else {
        cfg.trait_refresh_cycles
    };
    let result_cache_cycles = if cfg.result_cache_cycles == 0 {
        5
    } else {
        cfg.result_cache_cycles
    };

    let traits_cache_dir = cfg.traits_cache_dir.clone();
    let update_cfg = cfg.update_cfg;
    let state_store = cfg.state_store;
    let mut server_pubkey_pem = cfg.server_pubkey_pem;
    let data_dir = cfg.data_dir;
    let peer_map = cfg.peer_map;
    let manifest = cfg.manifest;
    let swarm_manager = cfg.swarm_manager;
    let gitback_gate = cfg.gitback_gate;

    let mut bo = Backoff::new();
    let mut last_fingerprint = String::new();
    let mut policy_state = PolicyState::new();
    let collectors = CachedCollectors::new(trait_refresh);
    let mut result_cache = ResultCache::new(result_cache_cycles);
    let mut pkg_breaker = runner::pkg_breaker::PackageBreaker::new();
    let mut pending_delivered = false;

    loop {
        if cancel.is_cancelled() {
            info!("daemon loop shutting down");
            return Ok(());
        }

        let cycle_start = Instant::now();

        // Collect traits for this cycle (including custom traits).
        let (traits_val, traits_json) = collect_traits(
            &collectors,
            client,
            &policy_state.custom_traits,
            traits_cache_dir.as_deref(),
        )
        .await;

        // Poll mode: run one unary check-in cycle.
        let lock_dir = data_dir.as_deref().unwrap_or(std::path::Path::new("/var/lib/vigo"));
        match run_once_with_update(
            client,
            executors,
            &traits_val,
            traits_json.clone(),
            &mut result_cache,
            &mut policy_state,
            false,
            None,
            &last_fingerprint,
            lock_dir,
            update_cfg.as_ref(),
            peer_map.as_ref(),
            manifest.as_ref(),
            state_store.as_deref(),
            &mut pkg_breaker,
            &server_pubkey_pem,
        )
        .await
        {
            Ok(rr) => {
                // Record success for failover tracking.
                client.record_success();

                // If currently failed over, periodically probe the primary.
                if client.is_failed_over() {
                    client.maybe_probe_primary().await;
                }

                // Bundle signature rejection: run_once_with_update verified
                // the bundle BEFORE substitution (the only correct order —
                // see its doc-comment). It returns here with the flag set so
                // we sleep the response's interval and try the next cycle
                // without accepting any bundle state.
                if rr.bundle_verify_failed {
                    let sleep_dur = jittered_duration(rr.interval, rr.jitter_pct);
                    info!(duration = ?sleep_dur, "sleeping until next check-in (bundle rejected)");
                    tokio::select! {
                        _ = cancel.cancelled() => return Ok(()),
                        _ = tokio::time::sleep(sleep_dur) => {}
                    }
                    continue;
                }

                // Update server pubkey if the server sent one.
                if !rr.server_pubkey_pem.is_empty() && rr.server_pubkey_pem != server_pubkey_pem {
                    info!("updating server bundle-signing public key");
                    server_pubkey_pem = rr.server_pubkey_pem.clone();
                    if let Some(dd) = data_dir.as_ref() {
                        let path = dd.join("server_pubkey.pem");
                        let _ = std::fs::write(&path, &server_pubkey_pem);
                    }
                }

                bo.reset();
                bo.set_max_from_interval(rr.interval);

                update_state(&mut policy_state, &rr, Some(&mut last_fingerprint));
                log_run_result(&rr);

                // Proxy convergence: converge network devices managed by this gateway.
                if !rr.proxied_devices.is_empty() {
                    converge_proxied_devices(client, &rr.proxied_devices, executors).await;
                }

                // Deliver pending results on successful reconnect.
                if !pending_delivered {
                    if let Some(ref store) = state_store {
                        deliver_pending(client, store).await;
                    }
                    pending_delivered = true;
                }

                // Accept bundle after successful online convergence.
                if let Some(ref store) = state_store {
                    accept_bundle(store, &policy_state, traits_json.as_deref());
                    sync_gitback_gate(gitback_gate.as_ref(), store);
                    crate::swarm::evict_expired(store);
                }
                crate::swarm::cleanup_graveyard();
                crate::swarm::cleanup_stale_partials();
                // Cache the fleet artifact catalog (ADR-024) so curator
                // resolution survives a server outage. Best-effort.
                if let Err(e) = crate::swarm::curator::catalog_cache::write_catalog(&rr.curator_catalog) {
                    tracing::warn!(error = %e, "curator: failed to cache the fleet catalog");
                }
                if let Some(ref m) = manifest {
                    crate::swarm::merge_manifest_entries(m, &rr.swarm_manifest_entries, peer_map.as_ref());
                    // Register local seeds into manifest for P2P gossip.
                    crate::swarm::register_seeds_in_manifest(m, client.envoy_id(), crate::swarm::server::DEFAULT_PORT);
                    m.prune_revoked(std::time::Duration::from_secs(7 * 24 * 3600));
                    // Trigger self-assignment evaluation after manifest update.
                    if let Some(ref mgr) = swarm_manager {
                        let mgr = mgr.clone();
                        tokio::spawn(async move { mgr.manifest_updated().await });
                    }
                }

                // Stream promotion: server has work to dispatch.
                if rr.stream_requested {
                    info!("server requested stream, opening for dispatched work");
                    let caps = state_store.as_ref().map(|_| StreamCapabilities {
                        supports_delta: true,
                        last_acked_sequence: 0,
                    });
                    let stream_req = client.build_check_in_request_with_caps(
                        &last_fingerprint,
                        traits_json.clone(),
                        policy_state.version,
                        policy_state.module_cache.hashes(),
                        caps,
                    );
                    // ADR-033: record the inventory the server now holds + honor a
                    // resend, once the handshake response is in.
                    let inv_hash = stream_req.inventory_hash.clone();

                    let mut agent_client = client.agent_client();
                    match stream::open(&mut agent_client, stream_req).await {
                        Ok((mut handle, initial_resp)) => {
                            info!("promoted stream connected");
                            client.commit_inventory(&inv_hash, initial_resp.resend_inventory);

                            // Deliver pending offline results on reconnect.
                            if !pending_delivered {
                                if let Some(ref store) = state_store {
                                    deliver_pending(client, store).await;
                                }
                            }

                            let exit_reason = stream_loop(
                                &cancel,
                                &mut handle,
                                client,
                                &initial_resp,
                                &traits_val,
                                executors,
                                &collectors,
                                &mut result_cache,
                                &mut policy_state,
                                &mut last_fingerprint,
                                state_store.as_deref(),
                                update_cfg.as_ref(),
                                peer_map.as_ref(),
                                manifest.as_ref(),
                                swarm_manager.as_ref(),
                                lock_dir,
                                &mut pkg_breaker,
                            )
                            .await;

                            // Cache state after stream disconnect.
                            if let Some(ref store) = state_store {
                                accept_bundle(store, &policy_state, traits_json.as_deref());
                                sync_gitback_gate(gitback_gate.as_ref(), store);
                            }

                            if cancel.is_cancelled() {
                                return Ok(());
                            }

                            match exit_reason {
                                StreamExit::Released => {
                                    info!("stream released, resuming polling");
                                    pending_delivered = false;
                                }
                                StreamExit::Disconnected(err) => {
                                    info!(error = ?err, "promoted stream disconnected, resuming polling");
                                    pending_delivered = false;
                                }
                                StreamExit::Cancelled => return Ok(()),
                            }
                            // Sleep before resuming polling to avoid tight loop
                            // when the stream is immediately released.
                            let sleep_dur = jittered_duration(rr.interval, rr.jitter_pct);
                            info!(duration = ?sleep_dur, "sleeping before resuming polling");
                            tokio::select! {
                                _ = cancel.cancelled() => return Ok(()),
                                _ = tokio::time::sleep(sleep_dur) => {}
                            }
                            continue; // back to top → next unary check-in
                        }
                        Err(e) => {
                            warn!(error = %e, "failed to open requested stream, resuming polling");
                        }
                    }
                }

                // Scrier tunnel_requested is now handled inside run_once_with_update
                // immediately after check-in, before convergence starts.

                // Proactive renewal: if bundle is approaching expiry (>80% of max_age),
                // reduce jitter to 0 for faster check-in.
                let effective_jitter = if policy_state.needs_renewal() {
                    info!(
                        version = policy_state.version,
                        age_secs = policy_state.bundle_age_secs(),
                        "bundle approaching expiry, prioritizing renewal"
                    );
                    0
                } else {
                    rr.jitter_pct
                };

                let target_dur = jittered_duration(rr.interval, effective_jitter);
                let elapsed = cycle_start.elapsed();
                let sleep_dur = target_dur.saturating_sub(elapsed);
                info!(
                    target = ?target_dur,
                    elapsed = ?elapsed,
                    sleep = ?sleep_dur,
                    "waiting for wake signal or next check-in"
                );

                // Use Wake RPC instead of sleeping. The server holds the
                // response until urgent work arrives (Scrier tunnel, task
                // dispatch, etc.) or the interval expires. Falls back to
                // normal sleep if the Wake RPC is unavailable.
                tokio::select! {
                    _ = cancel.cancelled() => return Ok(()),
                    _ = wake_or_sleep(client, sleep_dur) => {}
                }
            }
            Err(e) => {
                // Orphaned identity: the server returns NotFound when this
                // envoy id has no row (DB reset / deleted). Retrying a dead id
                // can never succeed without an operator re-bootstrap, so back
                // off long (REENROLL_BACKOFF) and say so plainly, rather than
                // hammering every cycle. Keep applying cached policy offline
                // meanwhile so the node doesn't drift.
                let reenroll = is_reenroll_required(&e);
                if reenroll {
                    warn!(
                        server = %client.active_server(),
                        "server does not recognize this envoy (unknown id) — re-bootstrap required: \
                         re-run the bootstrap install on this host \
                         (curl -sSfk https://<server>:8443/bootstrap | sudo sh); \
                         backing off, still applying cached policy offline"
                    );
                } else {
                    // Surface the underlying error before we fall through to
                    // failover/offline. Without this line, every check-in
                    // failure is invisible — the operator sees only the
                    // local-convergence retry log emitted further down and
                    // can't tell whether the server is unreachable, the TLS
                    // handshake is failing, the request signature is being
                    // rejected, or something else entirely.
                    warn!(
                        error = %e,
                        server = %client.active_server(),
                        "check-in failed, attempting failover before offline convergence"
                    );
                }
                // Failover helps only when the server is unreachable, not when
                // it affirmatively rejects an unknown id (peers share the same
                // enrollment state) — skip it for the re-enroll case.
                if !reenroll && client.record_failure().await {
                    // Failover occurred — retry immediately on the new server.
                    info!(server = %client.active_server(), "retrying on failover server");
                    continue;
                }

                // Server unreachable — attempt offline convergence.
                if let Some(ref store) = state_store {
                    if let Some((rr, _traits_bytes)) = converge_local(
                        store,
                        executors,
                        &mut result_cache,
                        &mut policy_state,
                        lock_dir,
                        &mut pkg_breaker,
                    ) {
                        log_run_result(&rr);
                        pending_delivered = false;

                        // Enqueue delta for delivery on reconnect.
                        if !rr.results.is_empty() {
                            if let Ok(mut wtxn) = store.write_txn() {
                                // A next_seq error must not collapse to seq 1 — that
                                // risks colliding with an already-queued delta on the
                                // offline path. Log and skip; the cached bundle re-
                                // derives this delta on the next cycle.
                                match store.pending.next_seq(&mut wtxn) {
                                    Ok(seq) => {
                                        let (delta, _run_id) = build_run_delta(&rr.results, seq as i64, policy_state.version);
                                        match store.pending.enqueue(&mut wtxn, &delta) {
                                            Ok(s) => {
                                                if let Err(e) = wtxn.commit() {
                                                    warn!(error = %e, "failed to commit offline delta transaction");
                                                } else {
                                                    info!(seq = s, "enqueued offline delta for later delivery");
                                                }
                                            }
                                            Err(e) => warn!(error = %e, "failed to enqueue offline delta"),
                                        }
                                    }
                                    Err(e) => warn!(error = %e, "failed to allocate offline delta seq — skipping enqueue this cycle"),
                                }
                            }
                        }

                        let wait = if reenroll { reenroll_backoff(bo.max()) } else { bo.next_backoff() };
                        info!(backoff = ?wait, "local convergence complete, waiting to retry server");
                        tokio::select! {
                            _ = cancel.cancelled() => return Ok(()),
                            _ = tokio::time::sleep(wait) => {}
                        }
                        continue;
                    }
                }

                let wait = if reenroll { reenroll_backoff(bo.max()) } else { bo.next_backoff() };
                warn!(error = %e, backoff = ?wait, "convergence run failed, backing off");

                tokio::select! {
                    _ = cancel.cancelled() => return Ok(()),
                    _ = tokio::time::sleep(wait) => {}
                }
            }
        }
    }
}

/// REENROLL_BACKOFF_FLOOR is the *minimum* retry interval after the server
/// reports this envoy as unknown (NotFound). Retrying a dead identity can't
/// succeed without an operator re-bootstrap, so we wait at least this long —
/// enough to keep applying cached policy offline without flooding the agent
/// log or the server's sig-verify metric.
const REENROLL_BACKOFF_FLOOR: Duration = Duration::from_secs(300);

/// reenroll_backoff is how long to wait between check-ins once the server has
/// disowned this envoy. It's the larger of the 5-minute floor and the normal
/// backoff ceiling (`bo.max()` = 2× the check-in interval) — so a fleet on a
/// long interval (e.g. 30m) doesn't get polled every 5 minutes, and a fleet on
/// a short interval (e.g. 5s) doesn't get hammered. Never faster than a healthy
/// agent's cadence; for a permanently-orphaned agent, slower.
fn reenroll_backoff(backoff_max: Duration) -> Duration {
    REENROLL_BACKOFF_FLOOR.max(backoff_max)
}

/// is_reenroll_required reports whether a check-in error is the server saying
/// it doesn't recognize this envoy id (gRPC NotFound). That's the orphaned-
/// identity case (server DB reset, or the row was deleted) — recoverable only
/// by re-bootstrapping, not by retrying. Distinct from Unauthenticated (bad
/// signature / clock skew) and transport failures, which a normal retry can
/// clear.
fn is_reenroll_required(err: &anyhow::Error) -> bool {
    err.chain().any(|cause| {
        cause
            .downcast_ref::<tonic::Status>()
            .is_some_and(|s| s.code() == tonic::Code::NotFound)
    })
}

/// Build a DeltaEvent with RunCompleted from convergence results.
/// Includes bundle_version so the server can correlate results to policy versions.
fn build_run_delta(results: &[ResourceResult], seq: i64, bundle_version: u64) -> (DeltaEvent, String) {
    let run_id = uuid::Uuid::new_v4().to_string();
    // Same filter the unary path uses: drop settled no-ops, retain
    // changed/failed/blocked rows, carry the true total. The server derives
    // counts + status from (results, total_count) via summarizeResults — one
    // blocked-aware definition for both report paths.
    let (filtered, total_count) = reportable_results(results);
    let (traceparent, _) = crate::checkin::traceparent::generate();
    let delta = DeltaEvent {
        sequence: seq,
        event: Some(delta_event::Event::Run(RunCompleted {
            run_id: run_id.clone(),
            total_count,
            results: filtered,
            bundle_version,
            traceparent,
        })),
    };

    (delta, run_id)
}

/// Send a DeltaEvent on the stream.
async fn send_delta(
    request_tx: &tokio::sync::mpsc::Sender<AgentStreamMessage>,
    delta: DeltaEvent,
) -> Result<()> {
    request_tx
        .send(AgentStreamMessage {
            msg: Some(agent_stream_message::Msg::Delta(delta)),
        })
        .await
        .map_err(|_| anyhow::anyhow!("stream closed"))?;
    Ok(())
}

/// Event-driven stream loop. Returns a StreamExit indicating why the loop ended.
async fn stream_loop(
    cancel: &tokio_util::sync::CancellationToken,
    handle: &mut stream::StreamHandle,
    client: &Client,
    initial_resp: &CheckInResponse,
    initial_traits: &Value,
    executors: &HashMap<String, Box<dyn Executor>>,
    collectors: &CachedCollectors,
    result_cache: &mut ResultCache,
    policy_state: &mut PolicyState,
    _last_fingerprint: &mut String,
    state_store: Option<&StateStore>,
    update_cfg: Option<&UpdateConfig>,
    peer_map: Option<&Arc<PeerMap>>,
    manifest: Option<&Arc<crate::swarm::manifest::Manifest>>,
    swarm_manager: Option<&crate::swarm::manager::SwarmManager>,
    data_dir: &std::path::Path,
    pkg_breaker: &mut runner::pkg_breaker::PackageBreaker,
) -> StreamExit {
    let use_delta = state_store.is_some();
    let mut delta_seq: i64 = 0;

    // Helper: report results over the stream (legacy path).
    let report = |request_tx: &tokio::sync::mpsc::Sender<AgentStreamMessage>,
                  client: &Client,
                  results: &[ResourceResult]| {
        let run_id = uuid::Uuid::new_v4().to_string();
        let (proto_results, total) = reportable_results(results);
        let req = client.build_report_result_request(&run_id, proto_results, total);
        let tx = request_tx.clone();
        async move {
            if let Err(e) = stream::StreamHandle::report_result(&tx, req).await {
                warn!(error = %e, "failed to report results on stream");
            }
        }
    };

    // Helper: merge manifest entries, reject, and signal the manager.
    // Self-assignment and revocation are driven entirely by the manifest CRDT —
    // the server no longer pushes per-envoy assignments or retired-seed lists.
    let spawn_swarm = |resp: &CheckInResponse| {
        if let Some(store) = state_store {
            crate::swarm::evict_expired(store);
        }
        crate::swarm::cleanup_graveyard();
        if let Err(e) = crate::swarm::curator::catalog_cache::write_catalog(&resp.curator_catalog) {
            tracing::warn!(error = %e, "curator: failed to cache the fleet catalog");
        }
        if let Some(m) = manifest {
            crate::swarm::merge_manifest_entries(m, &resp.manifest_entries, peer_map);
            // Register local seeds into manifest for P2P gossip.
            crate::swarm::register_seeds_in_manifest(m, client.envoy_id(), crate::swarm::server::DEFAULT_PORT);
            // Trigger self-assignment evaluation after manifest update.
            if let Some(mgr) = swarm_manager {
                let mgr = mgr.clone();
                tokio::spawn(async move { mgr.manifest_updated().await });
            }
        }
    };

    // Helper: self-update when the server requests it (before convergence).
    // force_self_update downloads the new binary and exec()s — must be awaited, not spawned.
    macro_rules! try_update {
        ($resp:expr) => {
            if $resp.force_update {
                if let Some(ucfg) = update_cfg {
                    if !ucfg.api_addr.is_empty() {
                        info!("server requested update via stream, downloading new binary");
                        crate::update::force_self_update(&ucfg.api_addr, &ucfg.current_version, ucfg.tls_certs.as_ref()).await;
                        // If we get here, exec() failed — continue on current binary.
                    }
                }
            }
        };
    }

    // Process initial check-in response.
    debug!(
        force_update = initial_resp.force_update,
        no_change = initial_resp.no_change,
        policy_version = initial_resp.policy_version,
        manifest_entries = initial_resp.manifest_entries.len(),
        "stream initial response received"
    );
    if let Err(e) = check_reassignment(initial_resp) {
        return StreamExit::Disconnected(Some(e));
    }
    spawn_swarm(initial_resp);
    try_update!(initial_resp);
    match converge(
        initial_resp, executors, initial_traits, result_cache, policy_state, false, None, data_dir, state_store, pkg_breaker,
    ) {
        Ok(rr) => {
            if !rr.results.is_empty() {
                if use_delta {
                    delta_seq += 1;
                    let (delta, _) = build_run_delta(&rr.results, delta_seq, policy_state.version);
                    if let Err(e) = send_delta(&handle.request_tx, delta).await {
                        warn!(error = %e, "failed to send delta on stream");
                    }
                } else {
                    report(&handle.request_tx, client, &rr.results).await;
                }
            }
            update_state(policy_state, &rr, None);
            log_run_result(&rr);
        }
        Err(e) => {
            warn!(error = %e, "initial stream convergence failed");
        }
    }

    // Determine periodic check-in interval.
    let mut interval = Duration::from_secs(300);
    let mut jitter_pct: i32 = 10;

    let next_checkin = tokio::time::sleep(jittered_duration(interval, jitter_pct));
    tokio::pin!(next_checkin);

    // Create a cancellation token tied to this stream's lifetime for push cleanup.
    let push_cancel = tokio_util::sync::CancellationToken::new();

    // Destructure handle so each field can be borrowed independently in select!.
    let stream::StreamHandle {
        ref request_tx,
        ref mut policy_push_rx,
        ref mut force_push_rx,
        ref mut check_in_resp_rx,
        ref mut task_dispatch_rx,
        ref mut query_request_rx,
        ref mut delta_ack_rx,
        ref mut stream_release_rx,
        ref mut tunnel_request_rx,
        ref mut done,
    } = *handle;

    let result: StreamExit = async {
        let mut cycle_start = Instant::now();
        loop {
            tokio::select! {
                _ = cancel.cancelled() => return StreamExit::Cancelled,

                _ = done.changed() => {
                    return StreamExit::Disconnected(Some(anyhow::anyhow!("stream disconnected")));
                }

                _ = stream_release_rx.recv() => {
                    info!("received StreamRelease, closing stream");
                    return StreamExit::Released;
                }

                push = policy_push_rx.recv() => {
                    let push = match push {
                        Some(p) => p,
                        None => return StreamExit::Disconnected(Some(anyhow::anyhow!("stream disconnected (policy push channel closed)"))),
                    };

                    info!(version = push.policy_version, "received policy push via stream");
                    let (traits_val, _) = collect_traits_basic(collectors).await;

                    let mut pushed_bundle = push.policy_bundle;
                    if let Some(bundle) = pushed_bundle.as_mut() {
                        // Stream-push runs on a &Client (no &mut handle to
                        // make the ResolveSecrets RPC here); substitute
                        // from the in-memory cache only. The check-in
                        // path that runs alongside this stream keeps the
                        // cache warm. If a placeholder isn't cached yet
                        // (cold-stream-arrival before any check-in), fail
                        // loud and let the next check-in fill the cache.
                        let cache = crate::policy::secrets::global_cache();
                        cache.invalidate_if_epoch_changed(bundle.secrets_epoch);
                        let refs = crate::policy::secrets::collect_refs_in_bundle_pub(bundle);
                        let missing = cache.missing_refs_pub(refs.iter().map(|s| s.as_str()));
                        if !missing.is_empty() {
                            warn!(
                                missing_count = missing.len(),
                                "stream push: {} placeholder secrets not yet cached, awaiting check-in",
                                missing.len()
                            );
                            continue;
                        }
                        if let Err(e) = crate::policy::secrets::substitute_in_bundle_pub(bundle, cache) {
                            warn!(error = %e, "stream push: secret substitution failed, skipping");
                            continue;
                        }
                    }
                    let synth_resp = CheckInResponse {
                        policy_bundle: pushed_bundle,
                        policy_version: push.policy_version,
                        ..Default::default()
                    };

                    match converge(&synth_resp, executors, &traits_val, result_cache, policy_state, false, None, data_dir, state_store, pkg_breaker) {
                        Ok(rr) => {
                            if !rr.results.is_empty() {
                                if use_delta {
                                    delta_seq += 1;
                                    let (delta, _) = build_run_delta(&rr.results, delta_seq, policy_state.version);
                                    if let Err(e) = send_delta(request_tx, delta).await {
                                        warn!(error = %e, "failed to send delta on stream");
                                    }
                                } else {
                                    report(request_tx, client, &rr.results).await;
                                }
                            }
                            update_state(policy_state, &rr, None);
                            log_run_result(&rr);
                        }
                        Err(e) => warn!(error = %e, "stream policy push convergence failed"),
                    }

                    next_checkin.as_mut().reset(Instant::now() + jittered_duration(interval, jitter_pct));
                    cycle_start = Instant::now();
                }

                fp = force_push_rx.recv() => {
                    let traceparent = fp.unwrap_or_default();
                    let trace_id = crate::checkin::traceparent::trace_id_of(&traceparent).unwrap_or("");
                    info!(trace_id = trace_id, "received force push via stream");
                    let (traits_val, traits_json) = collect_traits_basic(collectors).await;

                    let req = client.build_check_in_request(
                        "",
                        traits_json,
                        policy_state.version,
                        policy_state.module_cache.hashes(),
                    );
                    // ADR-033: record the inventory the server now holds + honor a
                    // resend, once the response is in (the builder is `&self`).
                    let inv_hash = req.inventory_hash.clone();

                    match stream::StreamHandle::check_in(request_tx, check_in_resp_rx, done, req).await {
                        Ok(resp) => {
                            client.commit_inventory(&inv_hash, resp.resend_inventory);
                            if let Err(e) = check_reassignment(&resp) {
                                return StreamExit::Disconnected(Some(e));
                            }
                            spawn_swarm(&resp);
                            try_update!(resp);
                            match converge(&resp, executors, &traits_val, result_cache, policy_state, false, None, data_dir, state_store, pkg_breaker) {
                                Ok(rr) => {
                                    if !rr.results.is_empty() {
                                        if use_delta {
                                            delta_seq += 1;
                                            let (delta, _) = build_run_delta(&rr.results, delta_seq, policy_state.version);
                                            if let Err(e) = send_delta(request_tx, delta).await {
                                                warn!(error = %e, "failed to send delta on stream");
                                            }
                                        } else {
                                            report(request_tx, client, &rr.results).await;
                                        }
                                    }
                                    update_state(policy_state, &rr, None);
                                    log_run_result(&rr);
                                }
                                Err(e) => warn!(error = %e, "stream force push convergence failed"),
                            }
                        }
                        Err(e) => warn!(error = %e, "stream check-in after force push failed"),
                    }

                    next_checkin.as_mut().reset(Instant::now() + jittered_duration(interval, jitter_pct));
                    cycle_start = Instant::now();
                }

                dispatch = task_dispatch_rx.recv() => {
                    let dispatch = match dispatch {
                        Some(d) => d,
                        None => continue, // channel closed, keep running
                    };

                    info!(task_id = %dispatch.task_id, command = %dispatch.command, "received task dispatch");

                    // Self-protection guardrails — reject dangerous commands.
                    if let Some(reason) = crate::guardrails::check_push_command(&dispatch.command) {
                        warn!(
                            task_id = %dispatch.task_id,
                            command = %dispatch.command,
                            reason = %reason,
                            "GUARDRAIL BLOCKED task dispatch"
                        );
                        crate::push::send_ack(request_tx, &dispatch.task_id, false, &format!("guardrail violation: {}", reason)).await;
                        continue;
                    }

                    // Acknowledge the push immediately.
                    crate::push::send_ack(request_tx, &dispatch.task_id, true, "").await;

                    // Execute the push synchronously (blocks this select arm,
                    // providing mutual exclusion with convergence).
                    let result = crate::push::execute(&dispatch, &push_cancel).await;

                    info!(
                        task_id = %result.task_id,
                        status = %result.status,
                        exit_code = result.exit_code,
                        duration_ms = result.duration_ms,
                        "push execution complete"
                    );

                    crate::push::send_result(request_tx, result).await;
                }

                query = query_request_rx.recv() => {
                    let query = match query {
                        Some(q) => q,
                        None => continue,
                    };

                    // Special virtual traits — read data on demand, not from cached traits.
                    let (value, found) = if query.trait_path == "security_scan_detail" {
                        match crate::traits::security_scan::collect_detail() {
                            Ok(v) if !v.is_null() => (serde_json::to_string(&v).unwrap_or_default(), true),
                            _ => (String::new(), false),
                        }
                    } else {
                        // Normal trait lookup from cached traits.
                        let (traits_val, _) = collect_traits_basic(collectors).await;
                        match crate::traits::traverse_dot_path(&traits_val, &query.trait_path) {
                            Some(v) => (serde_json::to_string(&v).unwrap_or_default(), true),
                            None => (String::new(), false),
                        }
                    };

                    // Send the response.
                    let resp = crate::proto::AgentStreamMessage {
                        msg: Some(crate::proto::agent_stream_message::Msg::QueryResponse(
                            crate::proto::QueryResponse {
                                query_id: query.query_id,
                                trait_path: query.trait_path,
                                value,
                                found,
                            },
                        )),
                    };
                    let _ = request_tx.send(resp).await;
                }

                // Drain delta acks (non-blocking).
                _ack = delta_ack_rx.recv() => {
                    // Delta acks confirm the server processed our delta.
                    // We don't need to act on them since we don't re-queue.
                }

                // Scrier: server requests us to open a TunnelStream.
                tunnel_req = tunnel_request_rx.recv() => {
                    if let Some(req) = tunnel_req {
                        info!(request_id = %req.request_id, "received tunnel request, opening TunnelStream");
                        // Spawn the tunnel on a separate connection (non-blocking).
                        match client.new_tunnel_client().await {
                            Ok(tunnel_client) => {
                                let envoy_id = client.envoy_id().to_string();
                                let (sig, ts) = client.sign(envoy_id.as_bytes());
                                tokio::spawn(async move {
                                    if let Err(e) = crate::tunnel::relay::run(tunnel_client, &envoy_id, sig, ts).await {
                                        warn!(error = %e, "tunnel relay failed");
                                    }
                                });
                            }
                            Err(e) => {
                                warn!(error = %e, "failed to create tunnel gRPC client");
                            }
                        }
                    }
                }

                _ = &mut next_checkin => {
                    // Periodic check-in.
                    let (traits_val, traits_json) = collect_traits_basic(collectors).await;

                    let req = client.build_check_in_request(
                        "",
                        traits_json,
                        policy_state.version,
                        policy_state.module_cache.hashes(),
                    );
                    // ADR-033: record the inventory the server now holds + honor a
                    // resend, once the response is in (the builder is `&self`).
                    let inv_hash = req.inventory_hash.clone();

                    match stream::StreamHandle::check_in(request_tx, check_in_resp_rx, done, req).await {
                        Ok(resp) => {
                            client.commit_inventory(&inv_hash, resp.resend_inventory);
                            if let Err(e) = check_reassignment(&resp) {
                                return StreamExit::Disconnected(Some(e));
                            }
                            spawn_swarm(&resp);
                            try_update!(resp);
                            match converge(&resp, executors, &traits_val, result_cache, policy_state, false, None, data_dir, state_store, pkg_breaker) {
                                Ok(rr) => {
                                    if !rr.results.is_empty() {
                                        if use_delta {
                                            delta_seq += 1;
                                            let (delta, _) = build_run_delta(&rr.results, delta_seq, policy_state.version);
                                            if let Err(e) = send_delta(request_tx, delta).await {
                                                warn!(error = %e, "failed to send delta on stream");
                                            }
                                        } else {
                                            report(request_tx, client, &rr.results).await;
                                        }
                                    } else if use_delta && rr.no_change {
                                        // Send heartbeat on no-change cycles.
                                        delta_seq += 1;
                                        let heartbeat = DeltaEvent {
                                            sequence: delta_seq,
                                            event: Some(delta_event::Event::Heartbeat(HeartbeatDelta {})),
                                        };
                                        if let Err(e) = send_delta(request_tx, heartbeat).await {
                                            warn!(error = %e, "failed to send heartbeat on stream");
                                        }
                                    }
                                    update_state(policy_state, &rr, None);
                                    if rr.interval > Duration::ZERO {
                                        interval = rr.interval;
                                    }
                                    if rr.jitter_pct > 0 {
                                        jitter_pct = rr.jitter_pct;
                                    }
                                    log_run_result(&rr);
                                }
                                Err(e) => warn!(error = %e, "stream periodic convergence failed"),
                            }
                        }
                        Err(e) => warn!(error = %e, "stream periodic check-in failed"),
                    }

                    let target = jittered_duration(interval, jitter_pct);
                    let remaining = target.saturating_sub(cycle_start.elapsed());
                    next_checkin.as_mut().reset(Instant::now() + remaining);
                    cycle_start = Instant::now();
                }
            }
        }
    }
    .await;

    // Cancel any in-flight push subprocess when the stream loop exits.
    push_cancel.cancel();

    result
}

/// Parse a Go-style duration string (e.g., "5m", "30s", "1h30m").
fn parse_go_duration(s: &str) -> Option<Duration> {
    let s = s.trim();
    if s.is_empty() {
        return None;
    }

    let mut total = Duration::ZERO;
    let mut num_buf = String::new();

    for ch in s.chars() {
        if ch.is_ascii_digit() || ch == '.' {
            num_buf.push(ch);
        } else {
            let val: f64 = num_buf.parse().ok()?;
            num_buf.clear();
            let mult = match ch {
                'h' => 3600.0,
                'm' => 60.0,
                's' => 1.0,
                _ => return None,
            };
            total += Duration::from_secs_f64(val * mult);
        }
    }

    if total == Duration::ZERO {
        None
    } else {
        Some(total)
    }
}

/// Converge network devices managed by this gateway agent.
///
/// For each proxied device, evaluates its policy bundle and runs convergence
/// using SSH-backed executors (ssh_exec, network_config). Results are reported
/// to the server under the device's virtual envoy_id.
async fn converge_proxied_devices(
    client: &mut Client,
    devices: &[ProxiedDevice],
    executors: &HashMap<String, Box<dyn Executor>>,
) {
    info!(count = devices.len(), "converging proxied network devices");

    for device in devices {
        let device_id = &device.envoy_id;
        let hostname = &device.hostname;
        let device_type = &device.device_type;

        info!(
            device_id,
            hostname,
            device_type,
            "converging proxied device"
        );

        // Evaluate the device's policy bundle. The bundle ships with
        // `__VIGO_SECRET__…__END_VIGO_SECRET__` placeholders wherever a
        // `secret:` ref appeared (including the device's SSH credential
        // delivered via ProxiedDevice.Credential, though the credential
        // itself is consumed below from `device.credential` not from
        // the bundle); substitute before eval_bundle sees the strings.
        let mut device_bundle = match device.policy.clone() {
            Some(b) => b,
            None => {
                warn!(hostname, "proxied device has no policy bundle, skipping");
                continue;
            }
        };
        if let Err(e) = crate::policy::secrets::resolve_and_substitute(
            &mut device_bundle,
            crate::policy::secrets::global_cache(),
            client,
        )
        .await
        {
            warn!(hostname, error = %e, "device secret substitution failed, skipping");
            continue;
        }
        let bundle = &device_bundle;

        // Collect device traits over SSH.
        let port = if device.port == 0 { 22 } else { device.port };
        let username_for_ssh = if device.username.is_empty() {
            "admin"
        } else {
            &device.username
        };
        let ip_for_ssh = if device.ip_address.is_empty() {
            hostname.as_str()
        } else {
            &device.ip_address
        };

        // Resolve a placeholder-bearing credential before handing it to
        // the SSH stack. ProxiedDevice.Credential ships as a placeholder
        // marker so the wire payload stays plaintext-free; substitute
        // here against the same cache the bundle's secrets ride on.
        let resolved_credential = match crate::policy::secrets::resolve_credential(
            &device.credential,
            crate::policy::secrets::global_cache(),
            client,
            bundle.secrets_epoch,
        )
        .await
        {
            Ok(c) => c,
            Err(e) => {
                warn!(hostname, error = %e, "device credential substitution failed, skipping");
                continue;
            }
        };

        let device_traits = match crate::transport::ssh::SshSession::connect(
            ip_for_ssh,
            port as u16,
            username_for_ssh,
            &resolved_credential,
            None,
        )
        .await
        {
            Ok(session) => {
                let traits = crate::traits::network_device::collect_device_traits(
                    &session,
                    device_type,
                )
                .await;

                // Report traits to server under device's envoy_id.
                let traits_bytes = serde_json::to_vec(&traits).unwrap_or_default();
                if !traits_bytes.is_empty() {
                    if let Err(e) = client.report_traits_for(device_id, &traits_bytes).await {
                        warn!(hostname, error = %e, "failed to report device traits");
                    }
                }

                let _ = session.close().await;
                traits
            }
            Err(e) => {
                warn!(hostname, error = %e, "SSH connect for trait collection failed");
                serde_json::json!({
                    "device": {
                        "type": device_type,
                        "hostname": hostname,
                    }
                })
            }
        };

        let modules = match crate::policy::eval::eval_bundle(Some(bundle), &device_traits) {
            Ok(m) => m,
            Err(e) => {
                warn!(hostname, error = %e, "device policy evaluation failed, skipping");
                continue;
            }
        };

        if modules.is_empty() {
            info!(hostname, "device has no modules after evaluation");
            continue;
        }

        // Inject SSH connection params into each resource's attributes.
        // The executors (ssh_exec, network_config) expect host/port/username/credential.
        let mut patched_modules = modules;
        for module in &mut patched_modules {
            for resource in &mut module.resources {
                resource.params.insert("host".to_string(), ip_for_ssh.to_string());
                resource.params.insert("port".to_string(), port.to_string());
                resource.params.insert("username".to_string(), username_for_ssh.to_string());
                resource.params.insert("credential".to_string(), resolved_credential.clone());
                resource.params.insert("device_type".to_string(), device_type.clone());
            }
        }

        let run_id = uuid::Uuid::new_v4().to_string();
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: run_id.clone(),
            stream_edit_allowed_paths: bundle.stream_edit_allowed_paths.clone(),
            stream_edit_timeout_secs: bundle.stream_edit_timeout_secs,
            data_dir: None, // network devices — preempt not applicable
        };

        let mut device_cache = ResultCache::new(1);
        let mut device_breaker = runner::pkg_breaker::PackageBreaker::new();
        let results = runner::converge(&patched_modules, executors, &mut device_cache, &opts, &device_traits, &mut device_breaker);

        let changed = results.iter().filter(|r| r.changed).count();
        let failed = results.iter().filter(|r| r.error.is_some()).count();

        info!(
            hostname,
            device_id,
            total = results.len(),
            changed,
            failed,
            "proxied device convergence complete"
        );

        // Report results under the device's envoy_id.
        if !results.is_empty() {
            let (proto_results, total) = reportable_results(&results);
            if let Err(e) = client
                .report_result_for(&run_id, device_id, proto_results, total)
                .await
            {
                warn!(
                    hostname,
                    device_id,
                    error = %e,
                    "failed to report proxied device results"
                );
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::PolicyConfigcrate;

    #[test]
    fn reenroll_required_only_on_notfound() {
        // NotFound = orphaned identity → re-enroll path.
        let nf = anyhow::Error::new(tonic::Status::not_found("unknown envoy"));
        assert!(is_reenroll_required(&nf));

        // Unauthenticated (bad signature / clock skew) is NOT re-enroll —
        // a normal retry can clear it.
        let unauth = anyhow::Error::new(tonic::Status::unauthenticated("bad signature"));
        assert!(!is_reenroll_required(&unauth));

        // A non-status (transport-ish) error is not re-enroll either.
        let other = anyhow::anyhow!("connection refused");
        assert!(!is_reenroll_required(&other));

        // Works through a wrapped/context'd error chain.
        let wrapped = anyhow::Error::new(tonic::Status::not_found("x")).context("check-in RPC");
        assert!(is_reenroll_required(&wrapped));
    }

    #[test]
    fn reenroll_backoff_floors_at_5min_then_tracks_interval() {
        // Short interval (bo.max ≈ 10s) → floored at 5 min, not hammered.
        assert_eq!(reenroll_backoff(Duration::from_secs(10)), Duration::from_secs(300));
        // Exactly at the floor.
        assert_eq!(reenroll_backoff(Duration::from_secs(300)), Duration::from_secs(300));
        // Long interval (bo.max = 2×30m = 60m) → respected; never polls a
        // disowned envoy faster than a healthy agent's cadence.
        assert_eq!(reenroll_backoff(Duration::from_secs(3600)), Duration::from_secs(3600));
    }

    #[test]
    fn jittered_within_range() {
        let base = Duration::from_secs(30);
        let pct = 20;
        let min = Duration::from_secs_f64(30.0 * 0.8);
        let max = Duration::from_secs_f64(30.0 * 1.2);

        for _ in 0..100 {
            let d = jittered_duration(base, pct);
            assert!(d >= min && d <= max, "got {:?}", d);
        }
    }

    #[test]
    fn jittered_zero_pct() {
        assert_eq!(jittered_duration(Duration::from_secs(30), 0), Duration::from_secs(30));
    }

    #[test]
    fn jittered_negative_pct() {
        assert_eq!(jittered_duration(Duration::from_secs(30), -5), Duration::from_secs(30));
    }

    #[test]
    fn parse_go_duration_basic() {
        assert_eq!(parse_go_duration("5m"), Some(Duration::from_secs(300)));
        assert_eq!(parse_go_duration("30s"), Some(Duration::from_secs(30)));
        assert_eq!(parse_go_duration("1h"), Some(Duration::from_secs(3600)));
        assert_eq!(parse_go_duration("1h30m"), Some(Duration::from_secs(5400)));
        assert_eq!(parse_go_duration(""), None);
    }

    #[test]
    fn strip_secret_triggers_removes_key() {
        let mut bundle = PolicyBundle {
            configcrates: vec![PolicyConfigcrate {
                name: "test".into(),
                stub: false,
                resources: vec![crate::proto::PolicyResource {
                    name: "res".into(),
                    r#type: "exec".into(),
                    attributes: {
                        let mut m = std::collections::BTreeMap::new();
                        m.insert("_secret_triggered".into(), "true".into());
                        m.insert("command".into(), "echo hi".into());
                        m
                    },
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        };

        strip_secret_triggers(&mut bundle);

        assert!(!bundle.configcrates[0].resources[0]
            .attributes
            .contains_key("_secret_triggered"));
        assert!(bundle.configcrates[0].resources[0]
            .attributes
            .contains_key("command"));
    }

    #[test]
    fn reportable_results_converts_and_elides_noops() {
        use crate::executors::Action;
        let results = vec![
            ResourceResult {
                configcrate_name: "nginx".into(),
                resource_type: "file".into(),
                resource_name: "/etc/nginx.conf".into(),
                action: Action::Updated,
                changed: true,
                error: None,
                duration_ms: 42,
                ..Default::default()
            },
            // A settled no-op (unchanged, no error, Action::None) — elided
            // from the rows but still counted in total.
            ResourceResult {
                configcrate_name: "nginx".into(),
                resource_type: "service".into(),
                resource_name: "nginx".into(),
                action: Action::None,
                changed: false,
                error: None,
                ..Default::default()
            },
        ];

        let (rows, total) = reportable_results(&results);
        assert_eq!(total, 2, "total counts every resource, incl. elided no-ops");
        assert_eq!(rows.len(), 1, "the settled no-op is elided from the rows");
        assert_eq!(rows[0].configcrate_name, "nginx");
        assert_eq!(rows[0].action_taken, "updated");
        assert!(rows[0].changed);
        assert!(rows[0].error_message.is_empty());
    }

    #[test]
    fn update_state_updates_version_and_fingerprint() {
        let mut ps = PolicyState::new();
        let mut fp = String::new();

        let rr = RunResult {
            results: vec![],
            interval: Duration::from_secs(300),
            jitter_pct: 10,
            no_change: false,
            response_fingerprint: "fp-abc".into(),
            policy_version: 42,
            policy_bundle: Some(PolicyBundle {
                policy_version: 42,
                ..Default::default()
            }),
            stream_requested: false,
            tunnel_requested: false,
            proxied_devices: vec![],
            server_pubkey_pem: String::new(),
            force_update: false,
            swarm_manifest_entries: vec![],
            curator_catalog: vec![],
            bundle_verify_failed: false,
        };

        update_state(&mut ps, &rr, Some(&mut fp));
        assert_eq!(ps.version, 42);
        assert_eq!(fp, "fp-abc");
        assert!(ps.accepted_bundle.is_some());
    }

    #[test]
    fn update_state_nil_fingerprint() {
        let mut ps = PolicyState::new();
        let rr = RunResult {
            results: vec![],
            interval: Duration::from_secs(300),
            jitter_pct: 10,
            no_change: false,
            response_fingerprint: "fp".into(),
            policy_version: 10,
            policy_bundle: None,
            stream_requested: false,
            tunnel_requested: false,
            proxied_devices: vec![],
            server_pubkey_pem: String::new(),
            force_update: false,
            swarm_manifest_entries: vec![],
            curator_catalog: vec![],
            bundle_verify_failed: false,
        };

        update_state(&mut ps, &rr, None);
        assert_eq!(ps.version, 10);
    }

    #[test]
    fn update_state_fills_stub_modules_before_accepting() {
        // Reproduces the offline-`vigo run` bug: after the first check-in the
        // server sends modules as delta-transfer stubs (name + hash, no
        // resources). `update_state` must persist the *resolved* bundle —
        // stubs filled from the module cache — not the raw wire bundle, or the
        // cached policy ends up with zero resources.
        let mut ps = PolicyState::new();
        // converge() warms the cache with this cycle's resolved modules.
        ps.module_cache.update(&[PolicyConfigcrate {
            name: "nginx".into(),
            content_hash: "h1".into(),
            resources: vec![crate::proto::PolicyResource {
                name: "conf".into(),
                r#type: "file".into(),
                attributes: {
                    let mut a = std::collections::BTreeMap::new();
                    a.insert("_secret_triggered".into(), "true".into());
                    a
                },
                ..Default::default()
            }],
            ..Default::default()
        }]);

        let rr = RunResult {
            results: vec![],
            interval: Duration::from_secs(300),
            jitter_pct: 10,
            no_change: false,
            response_fingerprint: "fp".into(),
            policy_version: 5,
            // The wire bundle as transmitted: nginx is a stub.
            policy_bundle: Some(PolicyBundle {
                policy_version: 5,
                configcrates: vec![PolicyConfigcrate {
                    name: "nginx".into(),
                    content_hash: "h1".into(),
                    stub: true,
                    resources: vec![],
                    ..Default::default()
                }],
                ..Default::default()
            }),
            stream_requested: false,
            tunnel_requested: false,
            proxied_devices: vec![],
            server_pubkey_pem: String::new(),
            force_update: false,
            swarm_manifest_entries: vec![],
            curator_catalog: vec![],
            bundle_verify_failed: false,
        };

        update_state(&mut ps, &rr, None);

        let accepted = ps.accepted_bundle.expect("bundle accepted");
        assert_eq!(accepted.configcrates.len(), 1);
        assert!(!accepted.configcrates[0].stub, "stub replaced with full module");
        assert_eq!(
            accepted.configcrates[0].resources.len(),
            1,
            "resources restored from the module cache"
        );
        assert!(
            !accepted.configcrates[0].resources[0]
                .attributes
                .contains_key("_secret_triggered"),
            "one-shot secret trigger stripped before persisting"
        );
        // The wire bundle itself is left untouched (verify_bundle relies on it).
        assert!(rr.policy_bundle.as_ref().unwrap().configcrates[0].stub);
    }

    #[test]
    fn accept_bundle_stores_and_loads() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let bundle = PolicyBundle {
            policy_version: 7,
            matched_pattern: "*.web".into(),
            environment: "staging".into(),
            configcrates: vec![PolicyConfigcrate {
                name: "nginx".into(),
                ..Default::default()
            }],
            ..Default::default()
        };

        let mut ps = PolicyState::new();
        ps.version = 7;
        ps.accepted_bundle = Some(bundle.clone());

        let traits_json = br#"{"os":"linux"}"#;
        accept_bundle(&store, &ps, Some(traits_json));

        // Verify bundle was persisted.
        let rtxn = store.read_txn().unwrap();
        let (ver, loaded) = store.policy.load(&rtxn).unwrap().unwrap();
        assert_eq!(ver, 7);
        assert_eq!(loaded.matched_pattern, "*.web");
        assert_eq!(loaded.configcrates.len(), 1);

        // Verify traits were persisted.
        let traits = store
            .traits
            .load(&rtxn, Duration::from_secs(3600))
            .unwrap()
            .unwrap();
        assert_eq!(traits, traits_json);
    }

    #[test]
    fn converge_local_uses_accepted_bundle() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        // Pre-populate LMDB with a bundle and traits.
        let bundle = PolicyBundle {
            policy_version: 5,
            configcrates: vec![PolicyConfigcrate {
                name: "test_mod".into(),
                ..Default::default()
            }],
            ..Default::default()
        };

        let traits_json = br#"{"os":{"family":"debian"}}"#;
        {
            let mut wtxn = store.write_txn().unwrap();
            store.policy.store(&mut wtxn, 5, &bundle).unwrap();
            store.traits.store(&mut wtxn, traits_json).unwrap();
            wtxn.commit().unwrap();
        }

        let executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        let mut result_cache = ResultCache::new(5);
        let mut ps = PolicyState::new();

        // converge_local should load the bundle and produce a result.
        let mut pkg_breaker = runner::pkg_breaker::PackageBreaker::new();
        let result = converge_local(&store, &executors, &mut result_cache, &mut ps, dir.path(), &mut pkg_breaker);
        assert!(result.is_some(), "converge_local should succeed with stored bundle");

        let (rr, traits_bytes) = result.unwrap();
        // With no executors matching the module, results may be empty — that's fine.
        // The important thing is that it ran without error.
        assert_eq!(rr.policy_version, 5);
        assert_eq!(traits_bytes, traits_json);

        // Policy state should be updated.
        assert_eq!(ps.version, 5);
        assert!(ps.accepted_bundle.is_some());
    }

    #[test]
    fn expired_bundle_skips_convergence() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        // Store a bundle with a short max_age.
        let bundle = PolicyBundle {
            policy_version: 3,
            max_age_seconds: 1, // 1 second max age
            configcrates: vec![PolicyConfigcrate {
                name: "test".into(),
                ..Default::default()
            }],
            ..Default::default()
        };

        let traits_json = br#"{}"#;
        {
            let mut wtxn = store.write_txn().unwrap();
            store.policy.store(&mut wtxn, 3, &bundle).unwrap();
            store.traits.store(&mut wtxn, traits_json).unwrap();
            wtxn.commit().unwrap();
        }

        let executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        let mut result_cache = ResultCache::new(5);
        let mut ps = PolicyState::new();
        // Simulate that bundle was accepted 10 seconds ago.
        ps.accepted_at = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs()
            - 10;

        let mut pkg_breaker = runner::pkg_breaker::PackageBreaker::new();
        let result = converge_local(&store, &executors, &mut result_cache, &mut ps, dir.path(), &mut pkg_breaker);
        assert!(result.is_none(), "should skip convergence for expired bundle");
    }

    #[test]
    fn build_run_delta_includes_bundle_version() {
        use crate::executors::Action;

        let results = vec![ResourceResult {
            configcrate_name: "nginx".into(),
            resource_type: "file".into(),
            resource_name: "/etc/nginx.conf".into(),
            action: Action::Updated,
            changed: true,
            error: None,
            duration_ms: 10,
            ..Default::default()
        }];

        let (delta, run_id) = build_run_delta(&results, 1, 42);
        assert!(!run_id.is_empty());

        match delta.event {
            Some(delta_event::Event::Run(ref run)) => {
                assert_eq!(run.bundle_version, 42);
                assert_eq!(run.total_count, 1);
                assert_eq!(run.results.len(), 1); // the changed row is retained
            }
            _ => panic!("expected RunCompleted delta event"),
        }
    }

    #[test]
    fn build_run_delta_retains_blocked() {
        use crate::executors::Action;

        let results = vec![
            ResourceResult {
                configcrate_name: "hardening".into(),
                resource_type: "exec".into(),
                resource_name: "danger".into(),
                action: Action::Blocked,
                changed: false,
                error: None,
                duration_ms: 1,
                ..Default::default()
            },
            ResourceResult {
                configcrate_name: "base".into(),
                resource_type: "file".into(),
                resource_name: "/etc/ok".into(),
                action: Action::None, // settled no-op — elided
                changed: false,
                error: None,
                duration_ms: 1,
                ..Default::default()
            },
        ];

        let (delta, _) = build_run_delta(&results, 1, 0);
        match delta.event {
            Some(delta_event::Event::Run(ref run)) => {
                assert_eq!(run.total_count, 2); // both resources counted
                assert_eq!(run.results.len(), 1); // only the blocked row kept
                assert_eq!(run.results[0].action_taken, "blocked");
            }
            _ => panic!("expected RunCompleted delta event"),
        }
    }

    #[test]
    fn update_state_sets_accepted_at() {
        let mut ps = PolicyState::new();
        assert_eq!(ps.accepted_at, 0);

        let rr = RunResult {
            results: vec![],
            interval: Duration::from_secs(300),
            jitter_pct: 10,
            no_change: false,
            response_fingerprint: String::new(),
            policy_version: 1,
            policy_bundle: Some(PolicyBundle {
                policy_version: 1,
                ..Default::default()
            }),
            stream_requested: false,
            tunnel_requested: false,
            proxied_devices: vec![],
            server_pubkey_pem: String::new(),
            force_update: false,
            swarm_manifest_entries: vec![],
            curator_catalog: vec![],
            bundle_verify_failed: false,
        };

        update_state(&mut ps, &rr, None);
        assert!(ps.accepted_at > 0, "accepted_at should be set on bundle acceptance");
    }

    #[test]
    fn bundle_expiry_and_renewal() {
        let mut ps = PolicyState::new();
        ps.accepted_bundle = Some(PolicyBundle {
            max_age_seconds: 100,
            ..Default::default()
        });

        // No accepted_at — not expired, no renewal.
        assert!(!ps.is_bundle_expired());
        assert!(!ps.needs_renewal());

        // Just accepted — not expired, no renewal.
        ps.accepted_at = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs();
        assert!(!ps.is_bundle_expired());
        assert!(!ps.needs_renewal());

        // 85% of max_age (85s ago) — needs renewal but not expired.
        ps.accepted_at = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs()
            - 85;
        assert!(!ps.is_bundle_expired());
        assert!(ps.needs_renewal());

        // 110% of max_age (110s ago) — expired AND needs renewal.
        ps.accepted_at = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs()
            - 110;
        assert!(ps.is_bundle_expired());
        assert!(ps.needs_renewal());
    }

    #[test]
    fn max_age_zero_never_expires() {
        let mut ps = PolicyState::new();
        ps.accepted_bundle = Some(PolicyBundle {
            max_age_seconds: 0, // No expiry
            ..Default::default()
        });
        ps.accepted_at = 1; // Very old

        assert!(!ps.is_bundle_expired());
        assert!(!ps.needs_renewal());
    }
}
