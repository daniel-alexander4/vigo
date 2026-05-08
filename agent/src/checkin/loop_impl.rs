// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Daemon loop with polling + streaming modes.
//!
//! Mirrors the Go `Loop()`, `streamLoop()`, `converge()`, `RunOnce()` functions.

use crate::checkin::backoff::Backoff;
use crate::checkin::client::Client;
use crate::checkin::modulecache::{merge_stubs, ModuleCache};
use crate::checkin::stream;
use crate::checkin::verify;
use crate::customtraits;
use crate::executors::{Executor, ResourceResult};
use crate::proto::{
    agent_stream_message, delta_event, AgentStreamMessage, CheckInResponse, CustomTraitEntry,
    DeltaEvent, HeartbeatDelta, ModuleResult, PolicyBundle, ProxiedDevice, RunCompleted,
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
    /// Live on/off switch for the gitmirror subsystem. Re-evaluated after
    /// every accepted policy bundle so operators can flip `gitmirror: true`
    /// in a matchfile without restarting the agent.
    pub gitmirror_gate: Option<crate::swarm::gitmirror::Gate>,
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
            gitmirror_gate: None,
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
}

/// Accepted policy state across convergence cycles.
pub struct PolicyState {
    pub version: u64,
    pub accepted_bundle: Option<PolicyBundle>,
    pub module_cache: ModuleCache,
    pub custom_traits: Vec<CustomTraitEntry>,
    /// When the current bundle was accepted (Unix seconds).
    pub accepted_at: u64,
}

impl PolicyState {
    pub fn new() -> Self {
        Self {
            version: 0,
            accepted_bundle: None,
            module_cache: ModuleCache::new(),
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

/// Convert agent ResourceResults to protobuf ModuleResults.
fn results_to_proto(results: &[ResourceResult]) -> Vec<ModuleResult> {
    results
        .iter()
        .map(|r| ModuleResult {
            module_name: r.module_name.clone(),
            resource_type: r.resource_type.clone(),
            resource_name: r.resource_name.clone(),
            action_taken: r.action.to_string(),
            changed: r.changed,
            error_message: r.error.clone().unwrap_or_default(),
            duration_ms: r.duration_ms,
            previous_content: r.previous_content.clone().unwrap_or_default(),
            content_hash: r.content_hash.clone().unwrap_or_default(),
            file_path: r.file_path.clone().unwrap_or_default(),
        })
        .collect()
}

/// Strip _secret_triggered from cached bundle to prevent re-firing on
/// subsequent no-change cycles.
fn strip_secret_triggers(bundle: &mut PolicyBundle) {
    for m in &mut bundle.modules {
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
            "reassignment received, agent should reconnect to new spoke"
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
        });
    };

    // Delta transfer: fill stub modules from cache.
    bundle.modules = merge_stubs(bundle.modules, &policy_state.module_cache);

    // Update module cache with fully resolved modules.
    policy_state.module_cache.update(&bundle.modules);

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
            });
        }
    };

    // Strip one-shot _secret_triggered flags.
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
        dry_run, module_filter, state_fingerprint, data_dir, None, None, None, pkg_breaker,
    ).await
}

/// Perform a single check-in cycle with optional pre-convergence self-update.
/// When update_cfg is provided and the server sets force_update, the agent
/// downloads the new binary and exec()s BEFORE convergence starts.
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
    pkg_breaker: &mut runner::pkg_breaker::PackageBreaker,
) -> Result<RunResult> {
    let resp = client
        .check_in(
            state_fingerprint,
            traits_json,
            policy_state.version,
            policy_state.module_cache.hashes(),
        )
        .await?;

    debug!(
        force_update = resp.force_update,
        no_change = resp.no_change,
        policy_version = resp.policy_version,
        manifest_entries = resp.manifest_entries.len(),
        "check-in response received"
    );

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
            "reassignment received, agent should reconnect to new spoke"
        );
        anyhow::bail!(
            "reassignment to {} with new ID {}",
            resp.reassign_addr,
            resp.reassign_id
        );
    }

    let rr = converge(
        &resp, executors, traits_val, cache, policy_state, dry_run, module_filter, data_dir, pkg_breaker,
    )?;

    // Report results back to server (skip in dry-run mode).
    if !dry_run && !rr.results.is_empty() {
        let run_id = uuid::Uuid::new_v4().to_string();
        let proto_results = results_to_proto(&rr.results);
        if let Err(e) = client.report_result(&run_id, proto_results).await {
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
    if let Some(ref bundle) = rr.policy_bundle {
        if !bundle.custom_traits.is_empty() {
            ps.custom_traits = bundle.custom_traits.clone();
        }
        ps.accepted_bundle = Some(bundle.clone());
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
    let mut wtxn = match store.write_txn() {
        Ok(t) => t,
        Err(e) => {
            warn!(error = %e, "failed to open write txn for pending drain");
            return;
        }
    };

    let pending = match store.pending.drain(&mut wtxn) {
        Ok(p) => p,
        Err(e) => {
            warn!(error = %e, "failed to drain pending results");
            return;
        }
    };

    if pending.is_empty() {
        let _ = wtxn.commit();
        return;
    }

    info!(count = pending.len(), "delivering pending offline results");

    // Extract results from delta events and report via legacy RPC.
    // (Pending delivery happens before stream is set up, so we use
    // the unary ReportResult RPC for simplicity.)
    for delta in &pending {
        if let Some(delta_event::Event::Run(ref run)) = delta.event {
            let proto_results = run.results.clone();
            if let Err(e) = client.report_result(&run.run_id, proto_results).await {
                warn!(run_id = %run.run_id, error = %e, "failed to deliver pending result");
            }
        }
    }

    if let Err(e) = wtxn.commit() {
        warn!(error = %e, "failed to commit pending drain");
    }
}

/// Re-read the gitmirror authorization vars from the freshly-accepted
/// bundle and propagate the result to the shared gate. Logs only on
/// transitions so the steady state is quiet.
fn sync_gitmirror_gate(
    gate: Option<&crate::swarm::gitmirror::Gate>,
    store: &StateStore,
) {
    let Some(gate) = gate else { return };
    let new_state = crate::swarm::gitmirror::gitmirror_enabled_for_envoy(Some(store));
    let prev = gate.set(new_state);
    if prev != new_state {
        if new_state {
            info!("gitmirror: enabled (this envoy now matches swarm.gitmirror.enabled; live flip, no restart required)");
        } else {
            info!("gitmirror: disabled (this envoy no longer matches swarm.gitmirror.enabled; live flip, reactor will idle)");
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

    // Build a synthetic response from accepted bundle.
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
    let gitmirror_gate = cfg.gitmirror_gate;

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
            &mut pkg_breaker,
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

                // Update server pubkey if the server sent one.
                if !rr.server_pubkey_pem.is_empty() && rr.server_pubkey_pem != server_pubkey_pem {
                    info!("updating server bundle-signing public key");
                    server_pubkey_pem = rr.server_pubkey_pem.clone();
                    if let Some(dd) = data_dir.as_ref() {
                        let path = dd.join("server_pubkey.pem");
                        let _ = std::fs::write(&path, &server_pubkey_pem);
                    }
                }

                // Verify bundle signature before accepting.
                if let Some(ref bundle) = rr.policy_bundle {
                    if let Err(e) = verify::verify_bundle(bundle, &server_pubkey_pem) {
                        warn!(error = %e, "bundle signature verification failed, rejecting");
                        // Use the server-provided interval (or default) instead of
                        // tight retry — the bundle will keep failing until the
                        // server pubkey is configured.
                        let sleep_dur = jittered_duration(rr.interval, rr.jitter_pct);
                        info!(duration = ?sleep_dur, "sleeping until next check-in (bundle rejected)");
                        tokio::select! {
                            _ = cancel.cancelled() => return Ok(()),
                            _ = tokio::time::sleep(sleep_dur) => {}
                        }
                        continue;
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
                    sync_gitmirror_gate(gitmirror_gate.as_ref(), store);
                    crate::swarm::evict_expired(store);
                }
                crate::swarm::cleanup_graveyard();
                crate::swarm::cleanup_stale_partials();
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

                    let mut agent_client = client.agent_client();
                    match stream::open(&mut agent_client, stream_req).await {
                        Ok((mut handle, initial_resp)) => {
                            info!("promoted stream connected");

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
                                sync_gitmirror_gate(gitmirror_gate.as_ref(), store);
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
                // Attempt failover to a fallback server before going offline.
                if client.record_failure().await {
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
                                let seq = store.pending.next_seq(&mut wtxn).unwrap_or(1) as i64;
                                let (delta, _run_id) = build_run_delta(&rr.results, seq, policy_state.version);
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
                        }

                        let wait = bo.next_backoff();
                        info!(backoff = ?wait, "local convergence complete, waiting to retry server");
                        tokio::select! {
                            _ = cancel.cancelled() => return Ok(()),
                            _ = tokio::time::sleep(wait) => {}
                        }
                        continue;
                    }
                }

                let wait = bo.next_backoff();
                warn!(error = %e, backoff = ?wait, "convergence run failed, backing off");

                tokio::select! {
                    _ = cancel.cancelled() => return Ok(()),
                    _ = tokio::time::sleep(wait) => {}
                }
            }
        }
    }
}

/// Build a DeltaEvent with RunCompleted from convergence results.
/// Includes bundle_version so the server can correlate results to policy versions.
fn build_run_delta(results: &[ResourceResult], seq: i64, bundle_version: u64) -> (DeltaEvent, String) {
    let run_id = uuid::Uuid::new_v4().to_string();
    let mut changed_count: i32 = 0;
    let mut failed_count: i32 = 0;
    let mut skipped_count: i32 = 0;

    // Filter to only changed/failed results (no-ops omitted).
    let filtered: Vec<ModuleResult> = results
        .iter()
        .filter(|r| r.changed || r.error.is_some())
        .map(|r| ModuleResult {
            module_name: r.module_name.clone(),
            resource_type: r.resource_type.clone(),
            resource_name: r.resource_name.clone(),
            action_taken: r.action.to_string(),
            changed: r.changed,
            error_message: r.error.clone().unwrap_or_default(),
            duration_ms: r.duration_ms,
            previous_content: r.previous_content.clone().unwrap_or_default(),
            content_hash: r.content_hash.clone().unwrap_or_default(),
            file_path: r.file_path.clone().unwrap_or_default(),
        })
        .collect();

    for r in results {
        if r.changed {
            changed_count += 1;
        }
        if r.error.is_some() {
            failed_count += 1;
        }
        if !r.changed && r.error.is_none() {
            skipped_count += 1;
        }
    }

    let status = if failed_count > 0 {
        "failure".to_string()
    } else {
        "success".to_string()
    };

    let delta = DeltaEvent {
        sequence: seq,
        event: Some(delta_event::Event::Run(RunCompleted {
            run_id: run_id.clone(),
            status,
            total_count: results.len() as i32,
            changed_count,
            failed_count,
            skipped_count,
            results: filtered,
            bundle_version,
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
        let proto_results = results_to_proto(results);
        let req = client.build_report_result_request(&run_id, proto_results);
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
        if let Some(ref store) = state_store {
            crate::swarm::evict_expired(store);
        }
        crate::swarm::cleanup_graveyard();
        if let Some(ref m) = manifest {
            crate::swarm::merge_manifest_entries(m, &resp.manifest_entries, peer_map);
            // Register local seeds into manifest for P2P gossip.
            crate::swarm::register_seeds_in_manifest(m, client.envoy_id(), crate::swarm::server::DEFAULT_PORT);
            // Trigger self-assignment evaluation after manifest update.
            if let Some(ref mgr) = swarm_manager {
                let mgr = (*mgr).clone();
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
        initial_resp, executors, initial_traits, result_cache, policy_state, false, None, data_dir, pkg_breaker,
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

                    let synth_resp = CheckInResponse {
                        policy_bundle: push.policy_bundle,
                        policy_version: push.policy_version,
                        ..Default::default()
                    };

                    match converge(&synth_resp, executors, &traits_val, result_cache, policy_state, false, None, data_dir, pkg_breaker) {
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

                _ = force_push_rx.recv() => {
                    info!("received force push via stream");
                    let (traits_val, traits_json) = collect_traits_basic(collectors).await;

                    let req = client.build_check_in_request(
                        "",
                        traits_json,
                        policy_state.version,
                        policy_state.module_cache.hashes(),
                    );

                    match stream::StreamHandle::check_in(request_tx, check_in_resp_rx, done, req).await {
                        Ok(resp) => {
                            if let Err(e) = check_reassignment(&resp) {
                                return StreamExit::Disconnected(Some(e));
                            }
                            spawn_swarm(&resp);
                            try_update!(resp);
                            match converge(&resp, executors, &traits_val, result_cache, policy_state, false, None, data_dir, pkg_breaker) {
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

                    match stream::StreamHandle::check_in(request_tx, check_in_resp_rx, done, req).await {
                        Ok(resp) => {
                            if let Err(e) = check_reassignment(&resp) {
                                return StreamExit::Disconnected(Some(e));
                            }
                            spawn_swarm(&resp);
                            try_update!(resp);
                            match converge(&resp, executors, &traits_val, result_cache, policy_state, false, None, data_dir, pkg_breaker) {
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

        // Evaluate the device's policy bundle.
        let bundle = match &device.policy {
            Some(b) => b,
            None => {
                warn!(hostname, "proxied device has no policy bundle, skipping");
                continue;
            }
        };

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

        let device_traits = match crate::transport::ssh::SshSession::connect(
            ip_for_ssh,
            port as u16,
            username_for_ssh,
            &device.credential,
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
                resource.params.insert("credential".to_string(), device.credential.clone());
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
            let proto_results = results_to_proto(&results);
            if let Err(e) = client
                .report_result_for(&run_id, device_id, proto_results)
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
    use crate::proto::PolicyModule;

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
            modules: vec![PolicyModule {
                name: "test".into(),
                stub: false,
                resources: vec![crate::proto::PolicyResource {
                    name: "res".into(),
                    r#type: "exec".into(),
                    attributes: {
                        let mut m = HashMap::new();
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

        assert!(!bundle.modules[0].resources[0]
            .attributes
            .contains_key("_secret_triggered"));
        assert!(bundle.modules[0].resources[0]
            .attributes
            .contains_key("command"));
    }

    #[test]
    fn results_to_proto_converts() {
        use crate::executors::Action;
        let results = vec![ResourceResult {
            module_name: "nginx".into(),
            resource_type: "file".into(),
            resource_name: "/etc/nginx.conf".into(),
            action: Action::Updated,
            changed: true,
            error: None,
            duration_ms: 42,
            ..Default::default()
        }];

        let proto = results_to_proto(&results);
        assert_eq!(proto.len(), 1);
        assert_eq!(proto[0].module_name, "nginx");
        assert_eq!(proto[0].action_taken, "updated");
        assert!(proto[0].changed);
        assert!(proto[0].error_message.is_empty());
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
        };

        update_state(&mut ps, &rr, None);
        assert_eq!(ps.version, 10);
    }

    #[test]
    fn accept_bundle_stores_and_loads() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let bundle = PolicyBundle {
            policy_version: 7,
            matched_pattern: "*.web".into(),
            environment: "staging".into(),
            modules: vec![PolicyModule {
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
        assert_eq!(loaded.modules.len(), 1);

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
            modules: vec![PolicyModule {
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
            modules: vec![PolicyModule {
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
            module_name: "nginx".into(),
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
                assert_eq!(run.changed_count, 1);
                assert_eq!(run.status, "success");
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
