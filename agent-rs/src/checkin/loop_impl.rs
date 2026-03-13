// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Daemon loop with polling + streaming modes.
//!
//! Mirrors the Go `Loop()`, `streamLoop()`, `converge()`, `RunOnce()` functions.

use crate::checkin::backoff::Backoff;
use crate::checkin::client::Client;
use crate::checkin::modulecache::{merge_stubs, ModuleCache};
use crate::checkin::stream;
use crate::customtraits;
use crate::executors::{Executor, ResourceResult};
use crate::proto::{
    agent_stream_message, delta_event, AgentStreamMessage, CheckInResponse, CustomTraitEntry,
    DeltaEvent, HeartbeatDelta, ModuleResult, PolicyBundle, RunCompleted,
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
use std::time::Duration;
use tokio::time::Instant;
use tracing::{debug, info, warn};

/// Configuration for the agent self-update mechanism.
pub struct UpdateConfig {
    pub api_addr: String,
    pub current_version: String,
}

/// Configuration for the daemon loop.
pub struct LoopConfig {
    pub trait_refresh_cycles: usize,
    pub result_cache_cycles: usize,
    pub traits_cache_dir: Option<String>,
    pub update_cfg: Option<UpdateConfig>,
    /// Directory for LMDB state store (enables offline convergence).
    /// When None, the agent behaves exactly as before — no persistent state.
    pub state_store: Option<StateStore>,
}

impl Default for LoopConfig {
    fn default() -> Self {
        Self {
            trait_refresh_cycles: 10,
            result_cache_cycles: 5,
            traits_cache_dir: None,
            update_cfg: None,
            state_store: None,
        }
    }
}

/// Result of a single convergence run.
pub struct RunResult {
    pub results: Vec<ResourceResult>,
    pub base_interval: Duration,
    pub jitter_pct: i32,
    pub no_change: bool,
    pub response_fingerprint: String,
    pub policy_version: u64,
    pub policy_bundle: Option<PolicyBundle>,
}

/// Cached policy state across check-in cycles.
pub struct PolicyState {
    pub version: u64,
    pub bundle: Option<PolicyBundle>,
    pub module_cache: ModuleCache,
    pub custom_traits: Vec<CustomTraitEntry>,
}

impl PolicyState {
    pub fn new() -> Self {
        Self {
            version: 0,
            bundle: None,
            module_cache: ModuleCache::new(),
            custom_traits: Vec::new(),
        }
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

/// Perform a convergence cycle with an already-received CheckInResponse.
///
/// Used by both RunOnce (poll) and the stream event loop.
fn converge(
    resp: &CheckInResponse,
    executors: &HashMap<String, Box<dyn Executor>>,
    traits_val: &Value,
    cache: &mut ResultCache,
    policy_state: &mut PolicyState,
    dry_run: bool,
    module_filter: Option<&str>,
) -> Result<RunResult> {
    // Parse server-provided timing.
    let base_interval = if resp.base_interval.is_empty() {
        Duration::from_secs(300) // 5m default
    } else {
        parse_go_duration(&resp.base_interval).unwrap_or(Duration::from_secs(300))
    };
    let jitter_pct = if resp.jitter_percent == 0 {
        10 // default
    } else {
        resp.jitter_percent
    };

    // Determine which policy bundle to evaluate.
    let mut bundle = if resp.no_change && policy_state.bundle.is_some() {
        // Config unchanged — evaluate cached bundle with fresh traits.
        policy_state.bundle.clone().unwrap()
    } else if let Some(ref b) = resp.policy_bundle {
        b.clone()
    } else if resp.no_change {
        // First check-in with no cached bundle and server said no_change.
        return Ok(RunResult {
            results: vec![],
            base_interval,
            jitter_pct,
            no_change: true,
            response_fingerprint: String::new(),
            policy_version: resp.policy_version,
            policy_bundle: None,
        });
    } else {
        return Ok(RunResult {
            results: vec![],
            base_interval,
            jitter_pct,
            no_change: false,
            response_fingerprint: resp.response_fingerprint.clone(),
            policy_version: resp.policy_version,
            policy_bundle: None,
        });
    };

    // Delta transfer: fill stub modules from cache.
    bundle.modules = merge_stubs(bundle.modules, &policy_state.module_cache);

    // Update module cache with fully resolved modules.
    policy_state.module_cache.update(&bundle.modules);

    // Evaluate the policy bundle with traits.
    let modules = match crate::policy::eval::eval_bundle(Some(&bundle), traits_val) {
        Ok(m) => m,
        Err(e) => anyhow::bail!("evaluating policy bundle: {}", e),
    };

    // Strip one-shot _secret_triggered flags.
    strip_secret_triggers(&mut bundle);

    if modules.is_empty() {
        warn!("server returned zero modules — check server logs for config errors (missing modules/roles) and verify this thrall is matched in thralls/");
        return Ok(RunResult {
            results: vec![],
            base_interval,
            jitter_pct,
            no_change: false,
            response_fingerprint: resp.response_fingerprint.clone(),
            policy_version: resp.policy_version,
            policy_bundle: resp.policy_bundle.clone(),
        });
    }

    let run_id = uuid::Uuid::new_v4().to_string();
    let opts = RunOptions {
        dry_run,
        module_filter: module_filter.map(|s| s.to_string()),
        run_id,
    };

    let results = runner::converge(&modules, executors, cache, &opts);

    Ok(RunResult {
        results,
        base_interval,
        jitter_pct,
        no_change: false,
        response_fingerprint: resp.response_fingerprint.clone(),
        policy_version: resp.policy_version,
        policy_bundle: resp.policy_bundle.clone(),
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
) -> Result<RunResult> {
    let resp = client
        .check_in(
            state_fingerprint,
            traits_json,
            policy_state.version,
            policy_state.module_cache.hashes(),
        )
        .await?;

    let rr = converge(
        &resp, executors, traits_val, cache, policy_state, dry_run, module_filter,
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

/// Update policy state from a run result.
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
        ps.bundle = Some(bundle.clone());
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

/// Cache policy and traits to the state store after a successful online run.
fn cache_state(store: &StateStore, policy_state: &PolicyState, traits_json: Option<&[u8]>) {
    if let Some(ref bundle) = policy_state.bundle {
        if let Ok(mut wtxn) = store.write_txn() {
            if let Err(e) = store.policy.store(&mut wtxn, policy_state.version, bundle) {
                warn!(error = %e, "failed to cache policy bundle");
            }
            if let Some(tj) = traits_json {
                if let Err(e) = store.traits.store(&mut wtxn, tj) {
                    warn!(error = %e, "failed to cache traits");
                }
            }
            let _ = wtxn.commit();
        }
    }
}

/// Attempt offline convergence using cached policy and traits.
/// Returns results to be enqueued for later delivery.
fn try_offline_converge(
    store: &StateStore,
    executors: &HashMap<String, Box<dyn Executor>>,
    result_cache: &mut ResultCache,
    policy_state: &mut PolicyState,
) -> Option<(RunResult, Vec<u8>)> {
    let rtxn = store.read_txn().ok()?;

    // Load cached policy.
    let (version, bundle) = store.policy.load(&rtxn).ok()??;

    // Load cached traits (any age — offline mode).
    let traits_bytes = store.traits.load_any(&rtxn).ok()??;
    drop(rtxn);

    let traits_val: Value = serde_json::from_slice(&traits_bytes).ok()?;

    info!(
        policy_version = version,
        "converging offline with cached policy"
    );

    // Build a synthetic response from cached data.
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
    ) {
        Ok(rr) => {
            // Update policy state so subsequent cycles use this bundle.
            policy_state.version = version;
            policy_state.bundle = Some(bundle);
            Some((rr, traits_bytes))
        }
        Err(e) => {
            warn!(error = %e, "offline convergence failed");
            None
        }
    }
}

/// Run the daemon loop until the cancellation token fires.
///
/// Attempts to open a persistent bidirectional stream first (push-based).
/// Falls back to polling if streaming is unavailable or disconnects.
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

    let mut bo = Backoff::new();
    let mut last_fingerprint = String::new();
    let mut policy_state = PolicyState::new();
    let collectors = CachedCollectors::new(trait_refresh);
    let mut result_cache = ResultCache::new(result_cache_cycles);
    let mut pending_delivered = false;

    loop {
        if cancel.is_cancelled() {
            info!("daemon loop shutting down");
            return Ok(());
        }

        // Collect traits for this cycle (including custom traits).
        let (traits_val, traits_json) = collect_traits(
            &collectors,
            client,
            &policy_state.custom_traits,
            traits_cache_dir.as_deref(),
        )
        .await;

        // Attempt stream mode.
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
                info!("stream connected, switching to push mode");
                bo.reset();

                // Deliver pending offline results on reconnect.
                if !pending_delivered {
                    if let Some(ref store) = state_store {
                        deliver_pending(client, store).await;
                    }
                    pending_delivered = true;
                }

                let stream_err = stream_loop(
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
                    state_store.as_ref(),
                )
                .await;

                // Cache state after stream disconnect.
                if let Some(ref store) = state_store {
                    cache_state(store, &policy_state, traits_json.as_deref());
                }

                if cancel.is_cancelled() {
                    return Ok(());
                }

                info!(error = ?stream_err, "stream disconnected, falling back to polling");
                pending_delivered = false;
                continue;
            }
            Err(e) => {
                debug!(error = %e, "stream not available, using polling");
            }
        }

        // Poll mode: run one cycle.
        match run_once(
            client,
            executors,
            &traits_val,
            traits_json.clone(),
            &mut result_cache,
            &mut policy_state,
            false,
            None,
            &last_fingerprint,
        )
        .await
        {
            Ok(rr) => {
                bo.reset();
                update_state(&mut policy_state, &rr, Some(&mut last_fingerprint));
                log_run_result(&rr);

                // Deliver pending offline results on successful reconnect.
                if !pending_delivered {
                    if let Some(ref store) = state_store {
                        deliver_pending(client, store).await;
                    }
                    pending_delivered = true;
                }

                // Cache state after successful online convergence.
                if let Some(ref store) = state_store {
                    cache_state(store, &policy_state, traits_json.as_deref());
                }

                // Self-update check (failures are logged, never crash the agent).
                if let Some(ref ucfg) = update_cfg {
                    if !ucfg.api_addr.is_empty() && !ucfg.current_version.is_empty() {
                        crate::update::check_self_update(&ucfg.api_addr, &ucfg.current_version).await;
                    }
                }

                let sleep_dur = jittered_duration(rr.base_interval, rr.jitter_pct);
                info!(
                    duration = ?sleep_dur,
                    "sleeping until next check-in"
                );

                tokio::select! {
                    _ = cancel.cancelled() => return Ok(()),
                    _ = tokio::time::sleep(sleep_dur) => {}
                }
            }
            Err(e) => {
                // Server unreachable — attempt offline convergence.
                if let Some(ref store) = state_store {
                    if let Some((rr, _traits_bytes)) = try_offline_converge(
                        store,
                        executors,
                        &mut result_cache,
                        &mut policy_state,
                    ) {
                        log_run_result(&rr);
                        pending_delivered = false;

                        // Enqueue delta for delivery on reconnect.
                        if !rr.results.is_empty() {
                            if let Ok(mut wtxn) = store.write_txn() {
                                let seq = store.pending.next_seq(&mut wtxn).unwrap_or(1) as i64;
                                let (delta, _run_id) = build_run_delta(&rr.results, seq);
                                match store.pending.enqueue(&mut wtxn, &delta) {
                                    Ok(s) => {
                                        let _ = wtxn.commit();
                                        info!(seq = s, "enqueued offline delta for later delivery");
                                    }
                                    Err(e) => warn!(error = %e, "failed to enqueue offline delta"),
                                }
                            }
                        }

                        let wait = bo.next_backoff();
                        info!(backoff = ?wait, "offline convergence complete, waiting to retry server");
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
fn build_run_delta(results: &[ResourceResult], seq: i64) -> (DeltaEvent, String) {
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

/// Event-driven stream loop. Returns when the stream disconnects or
/// the cancellation token fires.
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
) -> Result<()> {
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

    // Process initial check-in response.
    match converge(
        initial_resp, executors, initial_traits, result_cache, policy_state, false, None,
    ) {
        Ok(rr) => {
            if !rr.results.is_empty() {
                if use_delta {
                    delta_seq += 1;
                    let (delta, _) = build_run_delta(&rr.results, delta_seq);
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

    // Create a cancellation token tied to this stream's lifetime for task cleanup.
    let task_cancel = tokio_util::sync::CancellationToken::new();

    // Destructure handle so each field can be borrowed independently in select!.
    let stream::StreamHandle {
        ref request_tx,
        ref mut policy_push_rx,
        ref mut force_push_rx,
        ref mut check_in_resp_rx,
        ref mut task_dispatch_rx,
        ref mut query_request_rx,
        ref mut delta_ack_rx,
        ref mut done,
    } = *handle;

    let result = async {
        loop {
            tokio::select! {
                _ = cancel.cancelled() => return Ok(()),

                _ = done.changed() => {
                    anyhow::bail!("stream disconnected");
                }

                push = policy_push_rx.recv() => {
                    let push = match push {
                        Some(p) => p,
                        None => anyhow::bail!("stream disconnected (policy push channel closed)"),
                    };

                    info!(version = push.policy_version, "received policy push via stream");
                    let (traits_val, _) = collect_traits_basic(collectors).await;

                    let synth_resp = CheckInResponse {
                        policy_bundle: push.policy_bundle,
                        policy_version: push.policy_version,
                        ..Default::default()
                    };

                    match converge(&synth_resp, executors, &traits_val, result_cache, policy_state, false, None) {
                        Ok(rr) => {
                            if !rr.results.is_empty() {
                                if use_delta {
                                    delta_seq += 1;
                                    let (delta, _) = build_run_delta(&rr.results, delta_seq);
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
                            match converge(&resp, executors, &traits_val, result_cache, policy_state, false, None) {
                                Ok(rr) => {
                                    if !rr.results.is_empty() {
                                        if use_delta {
                                            delta_seq += 1;
                                            let (delta, _) = build_run_delta(&rr.results, delta_seq);
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
                }

                dispatch = task_dispatch_rx.recv() => {
                    let dispatch = match dispatch {
                        Some(d) => d,
                        None => continue, // channel closed, keep running
                    };

                    info!(task_id = %dispatch.task_id, command = %dispatch.command, "received task dispatch");

                    // Acknowledge the task immediately.
                    crate::task::send_ack(request_tx, &dispatch.task_id, true, "").await;

                    // Execute the task synchronously (blocks this select arm,
                    // providing mutual exclusion with convergence).
                    let result = crate::task::execute(&dispatch, &task_cancel).await;

                    info!(
                        task_id = %result.task_id,
                        status = %result.status,
                        exit_code = result.exit_code,
                        duration_ms = result.duration_ms,
                        "task execution complete"
                    );

                    crate::task::send_result(request_tx, result).await;
                }

                query = query_request_rx.recv() => {
                    let query = match query {
                        Some(q) => q,
                        None => continue,
                    };

                    // Collect traits (uses cache, so this is fast).
                    let (traits_val, _) = collect_traits_basic(collectors).await;

                    // Look up the requested trait path.
                    let (value, found) = match crate::traits::traverse_dot_path(&traits_val, &query.trait_path) {
                        Some(v) => (serde_json::to_string(&v).unwrap_or_default(), true),
                        None => (String::new(), false),
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
                            match converge(&resp, executors, &traits_val, result_cache, policy_state, false, None) {
                                Ok(rr) => {
                                    if !rr.results.is_empty() {
                                        if use_delta {
                                            delta_seq += 1;
                                            let (delta, _) = build_run_delta(&rr.results, delta_seq);
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
                                    if rr.base_interval > Duration::ZERO {
                                        interval = rr.base_interval;
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

                    next_checkin.as_mut().reset(Instant::now() + jittered_duration(interval, jitter_pct));
                }
            }
        }
    }
    .await;

    // Cancel any in-flight task subprocess when the stream loop exits.
    task_cancel.cancel();

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
            base_interval: Duration::from_secs(300),
            jitter_pct: 10,
            no_change: false,
            response_fingerprint: "fp-abc".into(),
            policy_version: 42,
            policy_bundle: Some(PolicyBundle {
                policy_version: 42,
                ..Default::default()
            }),
        };

        update_state(&mut ps, &rr, Some(&mut fp));
        assert_eq!(ps.version, 42);
        assert_eq!(fp, "fp-abc");
        assert!(ps.bundle.is_some());
    }

    #[test]
    fn update_state_nil_fingerprint() {
        let mut ps = PolicyState::new();
        let rr = RunResult {
            results: vec![],
            base_interval: Duration::from_secs(300),
            jitter_pct: 10,
            no_change: false,
            response_fingerprint: "fp".into(),
            policy_version: 10,
            policy_bundle: None,
        };

        update_state(&mut ps, &rr, None);
        assert_eq!(ps.version, 10);
    }
}
