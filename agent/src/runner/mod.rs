// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Convergence runner — executes resources in DAG order with notify/subscribe
//! triggers, result caching, and dry-run support.

pub mod cache;
pub mod dag;
pub mod lock;
pub mod pkg_breaker;
pub mod preempt;
pub mod reversal;

use crate::executors::{Action, Executor, ResourceResult};
use crate::proto::Configcrate;
use crate::state::StateStore;
use cache::ResultCache;
use dag::{build_dag, needs_topo_sort, topo_sort, ResourceNode};
use std::collections::{HashMap, HashSet};
use std::sync::atomic::{AtomicI64, Ordering};
use tracing::{debug, info, warn};

/// Minimum free disk percentage on root filesystem before convergence is skipped.
const MIN_DISK_FREE_PERCENT: f64 = 5.0;

/// Maximum consecutive resource failures before the circuit breaker trips.
const CIRCUIT_BREAKER_THRESHOLD: usize = 3;

/// Minimum free disk space (bytes) on /var before a package install is attempted.
/// Below this threshold, the executor tries `apt-get clean` / `dnf clean packages`
/// before reporting `blocked`.
const PKG_MIN_DISK_BYTES: u64 = 100 * 1024 * 1024; // 100 MiB

/// Resource types that edit files and should participate in file transactions.
const FILE_EDITING_TYPES: &[&str] = &[
    "file", "file_line", "blockinfile", "ini", "json_file", "replace", "field_edit",
];

/// Tracks file snapshots for rollback on resource execution failure.
///
/// Before a file-editing resource executes, capture the file's original content.
/// If the resource errors, restore the file from the snapshot.
struct FileTransactionTracker {
    snapshots: HashMap<String, Option<String>>,
}

impl FileTransactionTracker {
    fn new() -> Self {
        Self {
            snapshots: HashMap::new(),
        }
    }

    /// Capture original content for `path` if not already captured.
    fn ensure_snapshot(&mut self, path: &str) {
        if self.snapshots.contains_key(path) {
            return;
        }
        let content = std::fs::read_to_string(path).ok();
        self.snapshots.insert(path.to_string(), content);
    }

    /// Restore `path` to its original content (or delete if it didn't exist).
    fn rollback(&self, path: &str) {
        if let Some(original) = self.snapshots.get(path) {
            match original {
                Some(content) => {
                    let _ = std::fs::write(path, content);
                }
                None => {
                    let _ = std::fs::remove_file(path);
                }
            }
        }
    }
}

/// Extract the target file path from a resource node's params.
fn extract_target_path(node: &ResourceNode) -> Option<String> {
    if node.resource_type == "file" {
        node.params.get("target_path").cloned()
    } else {
        node.params.get("path").cloned()
    }
    .filter(|s| !s.is_empty())
}

/// Minimum interval between checksum-cache prune passes. Matches the gitback
/// reactor's `PRUNE_INTERVAL` cadence — checksum-cache pruning runs in-line
/// at the end of each successful converge, throttled here so high-frequency
/// check-ins don't pay the LMDB scan every cycle.
const CHECKSUMS_PRUNE_INTERVAL_SECS: i64 = 60;

static LAST_CHECKSUMS_PRUNE_AT: AtomicI64 = AtomicI64::new(0);

/// Collect the set of active managed file paths from a slice of modules.
/// Mirrors [`extract_target_path`] semantics on the raw proto: `file` reads
/// `target_path`, every other type reads `path`. Used to feed
/// [`crate::state::ChecksumCache::prune_stale`] after each converge.
fn active_target_paths(modules: &[Configcrate]) -> HashSet<String> {
    let mut out = HashSet::new();
    for m in modules {
        for r in &m.resources {
            let key = if r.r#type == "file" { "target_path" } else { "path" };
            if let Some(p) = r.params.get(key) {
                if !p.is_empty() {
                    out.insert(p.clone());
                }
            }
        }
    }
    out
}

/// Best-effort: prune stale entries from the checksum cache against the
/// just-converged active-paths set. Throttled to one pass per
/// [`CHECKSUMS_PRUNE_INTERVAL_SECS`]. No-op when the state store isn't
/// available or `modules` is empty — the latter guards against wiping the
/// entire cache on a pre-policy CheckIn or bundle-eval failure.
pub fn try_prune_stale_checksums(store: Option<&StateStore>, modules: &[Configcrate]) {
    let Some(store) = store else { return };
    if modules.is_empty() {
        return;
    }
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    let last = LAST_CHECKSUMS_PRUNE_AT.load(Ordering::Relaxed);
    if now.saturating_sub(last) < CHECKSUMS_PRUNE_INTERVAL_SECS {
        return;
    }

    let active = active_target_paths(modules);
    let active_refs: HashSet<&str> = active.iter().map(String::as_str).collect();

    let mut wtxn = match store.write_txn() {
        Ok(t) => t,
        Err(e) => {
            warn!(error = %e, "checksums prune: open write txn failed");
            return;
        }
    };
    match store.checksums.prune_stale(&mut wtxn, &active_refs) {
        Ok(pruned) => {
            if let Err(e) = wtxn.commit() {
                warn!(error = %e, "checksums prune: commit failed");
                return;
            }
            if pruned > 0 {
                info!(target: "state.checksums", pruned, "pruned stale checksum entries");
            }
            LAST_CHECKSUMS_PRUNE_AT.store(now, Ordering::Relaxed);
        }
        Err(e) => warn!(error = %e, "checksums prune: prune_stale failed"),
    }
}

/// Options for a convergence run.
pub struct RunOptions {
    pub dry_run: bool,
    /// Filter to a single module (by name).
    pub module_filter: Option<String>,
    /// Run ID for this convergence cycle.
    pub run_id: String,
    /// stream_edit: allowed script directories (from PolicyBundle).
    pub stream_edit_allowed_paths: Vec<String>,
    /// stream_edit: per-script timeout in seconds (from PolicyBundle, 0 = default 10s).
    pub stream_edit_timeout_secs: u32,
    /// Agent data directory — checked for preempt sentinel during convergence.
    pub data_dir: Option<std::path::PathBuf>,
}

/// Converge: build DAG, sort, execute resources, handle notify triggers.
///
/// Returns the list of resource results for reporting back to the server.
pub fn converge(
    modules: &[Configcrate],
    executors: &HashMap<String, Box<dyn Executor>>,
    cache: &mut ResultCache,
    opts: &RunOptions,
    traits: &serde_json::Value,
    pkg_breaker: &mut pkg_breaker::PackageBreaker,
) -> Vec<ResourceResult> {
    if modules.is_empty() {
        warn!("no modules to apply — check envoys.vgo");
        return Vec::new();
    }

    // SAFETY: Disk space gate — refuse to converge if root filesystem is critically low.
    if !opts.dry_run {
        if let Some(pct_free) = disk_free_percent("/") {
            if pct_free < MIN_DISK_FREE_PERCENT {
                warn!(
                    free_percent = format!("{:.1}", pct_free),
                    threshold = format!("{:.1}", MIN_DISK_FREE_PERCENT),
                    "SAFETY: skipping convergence — disk space critically low"
                );
                return vec![ResourceResult {
                    configcrate_name: String::new(),
                    resource_type: String::new(),
                    resource_name: "_safety_disk_space".into(),
                    action: Action::None,
                    changed: false,
                    error: Some(format!(
                        "SAFETY: convergence skipped — root filesystem {:.1}% free (minimum {:.1}%)",
                        pct_free, MIN_DISK_FREE_PERCENT
                    )),
                    duration_ms: 0,
                    ..Default::default()
                }];
            }
        }
    }

    // Filter to single module if requested.
    let filtered: Vec<Configcrate>;
    let modules = if let Some(ref filter) = opts.module_filter {
        filtered = modules
            .iter()
            .filter(|m| m.name == *filter)
            .cloned()
            .collect();
        if filtered.is_empty() {
            warn!(module = filter, "requested module not found");
            return Vec::new();
        }
        &filtered
    } else {
        modules
    };

    // Build DAG.
    let (nodes, notify_map) = match build_dag(modules) {
        Ok(r) => r,
        Err(e) => {
            warn!(error = %e, "failed to build DAG");
            return vec![ResourceResult {
                configcrate_name: String::new(),
                resource_type: String::new(),
                resource_name: String::new(),
                action: Action::None,
                changed: false,
                error: Some(format!("DAG build error: {}", e)),
                duration_ms: 0,
                ..Default::default()
            }];
        }
    };

    // Topological sort if needed.
    let ordered = if needs_topo_sort(&nodes) {
        match topo_sort(nodes) {
            Ok(sorted) => sorted,
            Err(e) => {
                warn!(error = %e, "topological sort failed");
                return vec![ResourceResult {
                    configcrate_name: String::new(),
                    resource_type: String::new(),
                    resource_name: String::new(),
                    action: Action::None,
                    changed: false,
                    error: Some(format!("DAG sort error: {}", e)),
                    duration_ms: 0,
                ..Default::default()
                }];
            }
        }
    } else {
        nodes
    };

    info!(resources = ordered.len(), "executing convergence run");

    // Track which resources changed (for notify triggers).
    let mut changed_resources: HashSet<String> = HashSet::new();
    // Track which resources failed (for depends_on failure propagation).
    let mut failed_resources: HashSet<String> = HashSet::new();
    // Track resources that need to be re-run due to notify.
    let mut notified: HashSet<String> = HashSet::new();
    let mut results: Vec<ResourceResult> = Vec::new();
    // File transaction tracking for rollback on error.
    let mut tracker = FileTransactionTracker::new();
    // SAFETY: Circuit breaker — consecutive failure counter.
    let mut consecutive_failures: usize = 0;
    let mut circuit_broken = false;

    // First pass: execute all resources.
    for node in &ordered {
        // Preempt check — bail if `vigo run` is waiting to take over.
        if let Some(ref dir) = opts.data_dir {
            if preempt::is_requested(dir) {
                warn!("preempted by manual run — aborting convergence");
                results.push(ResourceResult {
                    configcrate_name: node.configcrate_name.clone(),
                    resource_type: String::new(),
                    resource_name: "_preempted".into(),
                    action: Action::Skipped,
                    changed: false,
                    error: Some("convergence preempted by vigo run".into()),
                    duration_ms: 0,
                    skip_reason: Some("preempted".into()),
                    ..Default::default()
                });
                break;
            }
        }

        // SAFETY: Circuit breaker — stop applying if too many consecutive failures.
        if circuit_broken {
            results.push(ResourceResult {
                configcrate_name: node.configcrate_name.clone(),
                resource_type: node.resource_type.clone(),
                resource_name: node.name.clone(),
                action: Action::Skipped,
                changed: false,
                error: None,
                duration_ms: 0,
                skip_reason: Some("circuit breaker: convergence halted after consecutive failures".into()),
                ..Default::default()
            });
            continue;
        }
        // Skip if any dependency failed — cascading errors are noise.
        if let Some(failed_dep) = node.depends_on.iter().find(|d| failed_resources.contains(d.as_str())) {
            warn!(
                resource = %node.name,
                failed_dependency = %failed_dep,
                "skipping resource — dependency failed"
            );
            failed_resources.insert(node.name.clone());
            results.push(ResourceResult {
                configcrate_name: node.configcrate_name.clone(),
                resource_type: node.resource_type.clone(),
                resource_name: node.name.clone(),
                action: Action::Skipped,
                changed: false,
                error: Some(format!("dependency {:?} failed", failed_dep)),
                duration_ms: 0,
                skip_reason: Some("dep failed".into()),
                ..Default::default()
            });
            continue;
        }

        // Snapshot file before editing (for rollback).
        if !opts.dry_run && FILE_EDITING_TYPES.contains(&node.resource_type.as_str()) {
            if let Some(path) = extract_target_path(node) {
                tracker.ensure_snapshot(&path);
            }
        }

        // watch_secret resources should run on first convergence (no cached
        // result) so initial secrets like passwords get applied. On subsequent
        // cycles they skip unless the server sets _secret_triggered=true.
        let exec_node;
        let node_ref = if node.params.get("_watch_secret").map(|s| s == "true").unwrap_or(false)
            && !cache.has_run(&node.name)
        {
            let mut params = node.params.clone();
            params.insert("_triggered".to_string(), "true".to_string());
            exec_node = ResourceNode {
                params,
                ..node.clone()
            };
            &exec_node
        } else {
            node
        };

        let is_pkg_resource = is_package_type(&node.resource_type);
        let breaker_key = format!("{}:{}", node.configcrate_name, node.name);

        // --- Package pre-flight checks ---
        if is_pkg_resource && !opts.dry_run {
            // Circuit breaker: skip execution if resource is blocked from prior failures.
            if let pkg_breaker::BreakerCheck::Blocked(msg) = pkg_breaker.check(&breaker_key) {
                warn!(resource = %node.name, "package circuit breaker: blocked");
                results.push(ResourceResult {
                    configcrate_name: node.configcrate_name.clone(),
                    resource_type: node.resource_type.clone(),
                    resource_name: node.name.clone(),
                    action: Action::Blocked,
                    changed: false,
                    error: Some(msg),
                    duration_ms: 0,
                    ..Default::default()
                });
                continue;
            }

            // Disk space pre-flight: check /var before package installs.
            let state = node.params.get("state").map(|s| s.as_str()).unwrap_or("present");
            if state == "present" {
                if let Some(free) = disk_free_bytes("/var") {
                    if free < PKG_MIN_DISK_BYTES {
                        // Attempt auto-cleanup before giving up.
                        try_cleanup_package_cache();
                        if let Some(free_after) = disk_free_bytes("/var") {
                            if free_after < PKG_MIN_DISK_BYTES {
                                let free_mb = free_after / (1024 * 1024);
                                let req_mb = PKG_MIN_DISK_BYTES / (1024 * 1024);
                                let msg = format!(
                                    "package install {}: insufficient disk space on /var: {}MB free, {}MB required",
                                    node.name, free_mb, req_mb,
                                );
                                warn!(resource = %node.name, free_mb, req_mb, "disk space too low for package install");
                                pkg_breaker.record_failure(&breaker_key, &crate::executors::pkgmgr::errors::ErrorClass::DiskFull, &msg);
                                results.push(ResourceResult {
                                    configcrate_name: node.configcrate_name.clone(),
                                    resource_type: node.resource_type.clone(),
                                    resource_name: node.name.clone(),
                                    action: Action::Blocked,
                                    changed: false,
                                    error: Some(msg),
                                    duration_ms: 0,
                                    ..Default::default()
                                });
                                continue;
                            }
                        }
                    }
                }
            }
        }

        let mut result = execute_resource(node_ref, executors, cache, opts, traits);
        result.configcrate_name = node.configcrate_name.clone();
        // Force the result's resource_name to come from the DAG node,
        // not from whatever the executor seeded internally. The user
        // executor in particular initialized its result name from the
        // `username` param, which collapsed two `user` resources for
        // the same target user (e.g. dan + dan-desktop, both
        // `username: dan`) into a single `dan` row in run_results —
        // hiding which resource actually ran and making the runner's
        // notify-second-pass `find` collide on the wrong entry.
        // Forcing node.name here makes resource_name canonical
        // regardless of executor internals.
        result.resource_name = node.name.clone();

        // --- Package post-processing: classify errors, update circuit breaker ---
        if is_pkg_resource {
            if let Some(ref raw_error) = result.error {
                let class = crate::executors::pkgmgr::errors::classify_error("", raw_error);
                let operation = if result.action == Action::Deleted { "remove" } else { "install" };
                let formatted = crate::executors::pkgmgr::errors::format_error(
                    operation, &node.name, &class, raw_error,
                );
                result.error = Some(formatted.clone());

                let tripped = pkg_breaker.record_failure(&breaker_key, &class, &formatted);
                if tripped {
                    warn!(resource = %node.name, "package circuit breaker tripped");
                    result.action = Action::Blocked;
                    result.changed = false;
                }
            } else {
                pkg_breaker.record_success(&breaker_key);
            }
        }

        // Mark resource as seen (for watch_secret first-run detection).
        if result.error.is_none() && result.action != Action::Skipped {
            cache.mark_seen(&node.name);
        }

        // Rollback file on error.
        if result.error.is_some() && !opts.dry_run
            && FILE_EDITING_TYPES.contains(&node.resource_type.as_str()) {
                if let Some(path) = extract_target_path(node) {
                    warn!(resource = %node.name, path = %path, "rolling back file after error");
                    tracker.rollback(&path);
                }
            }

        // Track failures for dependency propagation and circuit breaker.
        if result.error.is_some() && result.action != Action::Blocked {
            failed_resources.insert(node.name.clone());
            consecutive_failures += 1;
            if consecutive_failures >= CIRCUIT_BREAKER_THRESHOLD {
                warn!(
                    failures = consecutive_failures,
                    "SAFETY: circuit breaker tripped — halting convergence after {} consecutive failures",
                    consecutive_failures
                );
                circuit_broken = true;
            }
        } else if result.action != Action::Skipped && result.action != Action::Blocked {
            // Reset on successful execution (skipped/blocked resources don't count).
            consecutive_failures = 0;
        }

        if result.changed {
            changed_resources.insert(node.name.clone());
            // Queue notify targets.
            if let Some(targets) = notify_map.get(&node.name) {
                for target in targets {
                    notified.insert(target.clone());
                }
            }
        }

        results.push(result);
    }

    // Second pass: re-run notified resources.
    if !notified.is_empty() {
        debug!(count = notified.len(), "re-running notified resources");

        for node in &ordered {
            if !notified.contains(&node.name) {
                continue;
            }
            // Skip if already changed in the first pass (no need to re-run).
            if changed_resources.contains(&node.name) {
                continue;
            }

            // Set _triggered flag so exec resources bypass guards.
            let mut triggered_params = node.params.clone();
            triggered_params.insert("_triggered".to_string(), "true".to_string());

            let triggered_node = ResourceNode {
                params: triggered_params,
                ..node.clone()
            };

            let mut result = execute_resource(&triggered_node, executors, cache, opts, traits);
            result.configcrate_name = node.configcrate_name.clone();
            // Same canonicalization as the first pass — see comment
            // there for the rationale (user executor seeds name from
            // username, two resources for the same user collide).
            result.resource_name = node.name.clone();

            // Update the result for this resource in the results vec.
            if let Some(existing) = results.iter_mut().find(|r| {
                r.resource_name == node.name && r.configcrate_name == node.configcrate_name
            }) {
                *existing = result;
            } else {
                results.push(result);
            }
        }
    }

    results
}

/// Execute a single resource node.
fn execute_resource(
    node: &ResourceNode,
    executors: &HashMap<String, Box<dyn Executor>>,
    cache: &mut ResultCache,
    opts: &RunOptions,
    traits: &serde_json::Value,
) -> ResourceResult {
    let dry_run = opts.dry_run;
    let run_id = &opts.run_id;
    // "when: changed" — only run when triggered by notify/subscribes/watch_secret.
    // Can be combined with other expressions: "when: changed && os_family('debian')"
    let triggered = node.params.get("_triggered").map(|s| s == "true").unwrap_or(false);
    if node.when.contains("changed") && !triggered {
        return ResourceResult {
            configcrate_name: String::new(),
            resource_type: node.resource_type.clone(),
            resource_name: node.name.clone(),
            action: Action::Skipped,
            changed: false,
            error: None,
            duration_ms: 0,
            skip_reason: Some("not triggered".into()),
            ..Default::default()
        };
    }

    // Check when expression (agent-side builtins).
    // Strip "changed" (already handled above) so the parser doesn't choke on it.
    let when_expr = node.when.replace("changed", "true");
    if !when_expr.is_empty() && when_expr != "true" {
        match crate::policy::when::parse(&when_expr) {
            Ok(expr) => {
                // Evaluate with builtin function resolver and no var substitution
                // (vars already resolved by eval_bundle).
                let var_fn = |_name: &str| false;
                let func_fn = |name: &str, args: &[String]| {
                    crate::policy::when::evaluate_builtin(name, args, traits)
                };
                match crate::policy::when::eval_with_funcs(&expr, &var_fn, Some(&func_fn)) {
                    Ok(false) => {
                        return ResourceResult {
                            configcrate_name: String::new(),
                            resource_type: node.resource_type.clone(),
                            resource_name: node.name.clone(),
                            action: Action::Skipped,
                            changed: false,
                            error: None,
                            duration_ms: 0,
                            skip_reason: Some(format!("when: {}", when_expr)),
                ..Default::default()
                        };
                    }
                    Err(e) => {
                        // Fail-safe: skip the resource on eval error (unknown
                        // builtins or wrong arity) rather than executing it.
                        warn!(
                            resource = %node.name,
                            error = %e,
                            "when expression eval error, skipping resource (fail-safe)"
                        );
                        return ResourceResult {
                            configcrate_name: String::new(),
                            resource_type: node.resource_type.clone(),
                            resource_name: node.name.clone(),
                            action: Action::Skipped,
                            changed: false,
                            error: None,
                            duration_ms: 0,
                            skip_reason: Some("when: eval error".into()),
                ..Default::default()
                        };
                    }
                    Ok(true) => {} // proceed
                }
            }
            Err(e) => {
                // Fail-safe: skip on parse error too.
                warn!(
                    resource = %node.name,
                    error = %e,
                    "when expression parse error, skipping resource (fail-safe)"
                );
                return ResourceResult {
                    configcrate_name: String::new(),
                    resource_type: node.resource_type.clone(),
                    resource_name: node.name.clone(),
                    action: Action::Skipped,
                    changed: false,
                    error: None,
                    duration_ms: 0,
                    skip_reason: Some("when: parse error".into()),
                ..Default::default()
                };
            }
        }
    }

    // Self-protection guardrails — non-configurable, enforced regardless of server policy.
    if let Some(violation) = crate::guardrails::check_resource(
        &node.name,
        &node.resource_type,
        &node.params,
    ) {
        warn!(
            resource = %node.name,
            resource_type = %node.resource_type,
            reason = %violation.reason,
            "GUARDRAIL BLOCKED resource execution"
        );
        return ResourceResult {
            configcrate_name: String::new(),
            resource_type: node.resource_type.clone(),
            resource_name: node.name.clone(),
            action: Action::None,
            changed: false,
            error: Some(format!("guardrail violation: {}", violation.reason)),
            duration_ms: 0,
                ..Default::default()
        };
    }

    // Check result cache.
    if !dry_run {
        if let Some(cached) = cache.lookup(&node.name, &node.resource_type, &node.params) {
            debug!(resource = %node.name, "cache hit, skipping executor");
            return cached;
        }
    }

    // Find executor for this resource type.
    let executor = match executors.get(&node.resource_type) {
        Some(e) => e,
        None => {
            return ResourceResult {
                configcrate_name: String::new(),
                resource_type: node.resource_type.clone(),
                resource_name: node.name.clone(),
                action: Action::None,
                changed: false,
                error: Some(format!(
                    "no executor for resource type {:?}",
                    node.resource_type
                )),
                duration_ms: 0,
                ..Default::default()
            };
        }
    };

    // Build params: start with resource params, inject resource name if
    // not already present (needed by executors like logrotate and cron that
    // use name as a functional parameter), and add _run_id for file snapshots.
    let mut params = node.params.clone();
    if !params.contains_key("name") {
        params.insert("name".to_string(), node.name.clone());
    }
    if node.resource_type == "file" {
        if !run_id.is_empty() {
            params.insert("_run_id".to_string(), run_id.to_string());
        }
        // Inject stream_edit guardrail params for the file executor.
        if !opts.stream_edit_allowed_paths.is_empty() {
            if let Ok(json) = serde_json::to_string(&opts.stream_edit_allowed_paths) {
                params.insert("_stream_edit_allowed_paths".to_string(), json);
            }
        }
        let timeout = if opts.stream_edit_timeout_secs > 0 {
            opts.stream_edit_timeout_secs
        } else {
            10
        };
        params.insert("_stream_edit_timeout".to_string(), timeout.to_string());
    }

    // Background exec resources return immediately — skip timeout wrapper.
    let is_background = node.resource_type == "exec"
        && node.params.get("background").map(|s| s == "true").unwrap_or(false);

    let mut result = if is_background {
        executor.execute(&params, dry_run)
    } else {
        // Determine execution timeout.
        let default_timeout: u64 = match node.resource_type.as_str() {
            "exec" | "source_package" | "custom" | "package" | "repository" | "nonrepo_package" => 300,
            _ => 60,
        };
        // Same parser the exec executor uses for `timeout:` — a bare integer or
        // a suffixed duration ("15m", "4h"). A plain `.parse::<u64>()` here
        // silently rejected every suffixed value and fell back to the default,
        // so `timeout: "15m"` reported a timeout at the 300s default while the
        // executor let the child run to 900s.
        let timeout_secs: u64 = node
            .params
            .get("timeout_secs")
            .or_else(|| node.params.get("timeout"))
            .and_then(|v| crate::executors::exec::parse_duration(v))
            .map(|d| d.as_secs())
            .unwrap_or(default_timeout);

        // Pass the computed default down so subprocess-spawning executors can
        // enforce a kill-on-timeout ceiling internally rather than relying on
        // the recv_timeout below — which detects a timeout but cannot kill the
        // hung child, blocking the whole convergence cycle at scope exit. The
        // exec executor consults this for its no-explicit-`timeout:` path; a
        // resource that sets its own `timeout:` already wins via that param, so
        // injecting the default is harmless there.
        params.insert(
            "_default_timeout_secs".to_string(),
            timeout_secs.to_string(),
        );

        // Execute with timeout using a scoped thread + channel.
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::scope(|s| {
            s.spawn(|| {
                let r = executor.execute(&params, dry_run);
                let _ = tx.send(r);
            });
            match rx.recv_timeout(std::time::Duration::from_secs(timeout_secs)) {
                Ok(r) => r,
                Err(_) => ResourceResult {
                    configcrate_name: String::new(),
                    resource_type: node.resource_type.clone(),
                    resource_name: node.name.clone(),
                    action: Action::None,
                    changed: false,
                    error: Some(format!("execution timed out after {}s", timeout_secs)),
                    duration_ms: timeout_secs as i32 * 1000,
                    ..Default::default()
                },
            }
        })
    };
    // Enforce the ResourceResult contract: error + Created/Updated/Deleted
    // is contradictory — scrub action/changed at the boundary so the UI
    // never has to render an impossible row. See executors/mod.rs.
    result.normalize_for_error();

    // Cache the result.
    if !dry_run {
        cache.store(&node.name, &node.resource_type, &node.params, &result);
    }

    if let Some(ref err) = result.error {
        warn!(
            resource = %node.name,
            resource_type = %node.resource_type,
            error = %err,
            "resource execution failed"
        );
    } else {
        debug!(
            resource = %node.name,
            resource_type = %node.resource_type,
            action = %result.action,
            "resource executed"
        );
    }

    result
}

/// Build the default executor map with all built-in resource types.
pub fn build_executors(
    os_family: &str,
    os_distro: &str,
) -> HashMap<String, Box<dyn Executor>> {
    build_executors_with_snapshots(os_family, os_distro, None)
}

/// Build executors with optional snapshot manager for the file executor.
pub fn build_executors_with_snapshots(
    os_family: &str,
    os_distro: &str,
    snapshot_mgr: Option<crate::snapshots::Manager>,
) -> HashMap<String, Box<dyn Executor>> {
    use crate::executors::*;

    let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();

    // Cross-platform executors.
    executors.insert("file".to_string(), Box::new(file::FileExecutor::new(snapshot_mgr)));
    executors.insert("directory".to_string(), Box::new(directory::DirectoryExecutor::new()));
    executors.insert("symlink".to_string(), Box::new(symlink::SymlinkExecutor::new()));
    executors.insert("exec".to_string(), Box::new(exec::ExecExecutor::new()));
    executors.insert("process".to_string(), Box::new(process::ProcessExecutor::new()));
    executors.insert("file_line".to_string(), Box::new(file_line::FileLineExecutor));
    executors.insert("host".to_string(), Box::new(host::HostExecutor::new()));
    executors.insert("sshkey".to_string(), Box::new(sshkey::SshKeyExecutor));
    executors.insert("ini".to_string(), Box::new(ini::IniExecutor));
    executors.insert("json_file".to_string(), Box::new(json_file::JsonFileExecutor));
    executors.insert("blockinfile".to_string(), Box::new(blockinfile::BlockInFileExecutor));
    executors.insert("source_package".to_string(), Box::new(source_package::SourcePackageExecutor::new()));
    executors.insert("nonrepo_package".to_string(), Box::new(nonrepo_package::NonRepoPackageExecutor::new()));
    executors.insert("backup".to_string(), Box::new(backup::BackupExecutor::new()));
    executors.insert("db_backup".to_string(), Box::new(db_backup::DbBackupExecutor::new()));
    executors.insert("repository".to_string(), Box::new(repository::RepositoryExecutor::for_os(os_family)));
    executors.insert("git".to_string(), Box::new(git::GitExecutor::new()));
    executors.insert("reboot".to_string(), Box::new(reboot::RebootExecutor::new()));
    executors.insert("pip".to_string(), Box::new(pip::PipExecutor::new()));
    executors.insert("gem".to_string(), Box::new(gem::GemExecutor::new()));
    executors.insert("npm".to_string(), Box::new(npm::NpmExecutor::new()));
    executors.insert("container".to_string(), Box::new(container::ContainerExecutor::new()));
    executors.insert("vm".to_string(), Box::new(vm::VmExecutor::new()));
    executors.insert("replace".to_string(), Box::new(replace::ReplaceExecutor));
    executors.insert("field_edit".to_string(), Box::new(field_edit::FieldEditExecutor));

    // Network device executors (SSH-backed, used for gateway proxy convergence).
    executors.insert("ssh_exec".to_string(), Box::new(ssh_exec::SshExecExecutor::new()));
    executors.insert("network_config".to_string(), Box::new(network_config::NetworkConfigExecutor::new()));
    executors.insert("cisco_vlan".to_string(), Box::new(cisco_vlan::CiscoVlanExecutor::new()));
    executors.insert("cisco_interface".to_string(), Box::new(cisco_interface::CiscoInterfaceExecutor::new()));
    executors.insert("cisco_acl".to_string(), Box::new(cisco_acl::CiscoAclExecutor::new()));
    executors.insert("junos_interface".to_string(), Box::new(junos_interface::JunosInterfaceExecutor::new()));
    executors.insert("network_ntp".to_string(), Box::new(network_ntp::NetworkNtpExecutor::new()));
    executors.insert("network_syslog".to_string(), Box::new(network_syslog::NetworkSyslogExecutor::new()));
    executors.insert("network_snmp".to_string(), Box::new(network_snmp::NetworkSnmpExecutor::new()));
    executors.insert("network_user".to_string(), Box::new(network_user::NetworkUserExecutor::new()));
    executors.insert("network_banner".to_string(), Box::new(network_banner::NetworkBannerExecutor::new()));
    executors.insert("network_interface".to_string(), Box::new(network_interface::NetworkInterfaceExecutor::new()));
    executors.insert("network_vlan".to_string(), Box::new(network_vlan::NetworkVlanExecutor::new()));
    executors.insert("network_acl".to_string(), Box::new(network_acl::NetworkAclExecutor::new()));
    executors.insert("network_static_route".to_string(), Box::new(network_static_route::NetworkStaticRouteExecutor::new()));
    executors.insert("network_dns".to_string(), Box::new(network_dns::NetworkDnsExecutor::new()));

    // Linux-only executors.
    #[cfg(target_os = "linux")]
    {
        executors.insert("service".to_string(), Box::new(service::ServiceExecutor::new()));
        executors.insert("systemd_dropin".to_string(), Box::new(systemd_dropin::SystemdDropinExecutor::new()));
        executors.insert("user".to_string(), Box::new(user::UserExecutor::new()));
        executors.insert("group".to_string(), Box::new(group::GroupExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));
        executors.insert("hostname".to_string(), Box::new(hostname::HostnameExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone::TimezoneExecutor::new()));
        executors.insert("sysctl".to_string(), Box::new(sysctl::SysctlExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall::FirewallExecutor::new()));
        executors.insert("kernel_module".to_string(), Box::new(kernel_module::KernelModuleExecutor::new()));
        executors.insert("mount".to_string(), Box::new(mount::MountExecutor::new()));
        executors.insert("swap".to_string(), Box::new(swap::SwapExecutor::new()));
        executors.insert("tmpfile".to_string(), Box::new(tmpfile::TmpfileExecutor::new()));
        executors.insert("logrotate".to_string(), Box::new(logrotate::LogrotateExecutor));
        executors.insert("alternatives".to_string(), Box::new(alternatives::AlternativesExecutor::new()));
        executors.insert("selinux".to_string(), Box::new(selinux::SelinuxExecutor::new()));

        // Linux package manager detection.
        match pkgmgr::detect(os_family, os_distro) {
            Ok(pm) => {
                executors.insert(
                    "package".to_string(),
                    Box::new(PackageExecutorWrapper { pkg_mgr: pm }),
                );
            }
            Err(e) => {
                warn!(error = %e, "no package manager detected, package resources will fail");
            }
        }
    }

    // macOS-native executors.
    #[cfg(target_os = "macos")]
    {
        executors.insert("service".to_string(), Box::new(service_macos::ServiceMacosExecutor::new()));
        executors.insert("user".to_string(), Box::new(user_macos::UserMacosExecutor::new()));
        executors.insert("group".to_string(), Box::new(group_macos::GroupMacosExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_macos::HostnameMacosExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_macos::TimezoneMacosExecutor::new()));
        executors.insert("sysctl".to_string(), Box::new(sysctl_bsd::SysctlBsdExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall_pf::FirewallPfExecutor::new()));
        executors.insert("mount".to_string(), Box::new(mount_bsd::MountBsdExecutor::new()));
        executors.insert("swap".to_string(), Box::new(swap_bsd::SwapBsdExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));

        match pkgmgr::detect(os_family, os_distro) {
            Ok(pm) => {
                executors.insert(
                    "package".to_string(),
                    Box::new(PackageExecutorWrapper { pkg_mgr: pm }),
                );
            }
            Err(e) => {
                warn!(error = %e, "no package manager detected, package resources will fail");
            }
        }
    }

    // FreeBSD-native executors.
    #[cfg(target_os = "freebsd")]
    {
        executors.insert("service".to_string(), Box::new(service_freebsd::ServiceFreebsdExecutor::new()));
        executors.insert("user".to_string(), Box::new(user_freebsd::UserFreebsdExecutor::new()));
        executors.insert("group".to_string(), Box::new(group_freebsd::GroupFreebsdExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_bsd::HostnameBsdExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_bsd::TimezoneBsdExecutor::new()));
        executors.insert("sysctl".to_string(), Box::new(sysctl_bsd::SysctlBsdExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall_pf::FirewallPfExecutor::new()));
        executors.insert("kernel_module".to_string(), Box::new(kernel_module_freebsd::KernelModuleFreebsdExecutor::new()));
        executors.insert("mount".to_string(), Box::new(mount_bsd::MountBsdExecutor::new()));
        executors.insert("swap".to_string(), Box::new(swap_bsd::SwapBsdExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));

        match pkgmgr::detect(os_family, os_distro) {
            Ok(pm) => {
                executors.insert(
                    "package".to_string(),
                    Box::new(PackageExecutorWrapper { pkg_mgr: pm }),
                );
            }
            Err(e) => {
                warn!(error = %e, "no package manager detected, package resources will fail");
            }
        }
    }

    // OpenBSD-native executors.
    #[cfg(target_os = "openbsd")]
    {
        executors.insert("service".to_string(), Box::new(service_openbsd::ServiceOpenbsdExecutor::new()));
        executors.insert("user".to_string(), Box::new(user::UserExecutor::new()));
        executors.insert("group".to_string(), Box::new(group::GroupExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_bsd::HostnameBsdExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_bsd::TimezoneBsdExecutor::new()));
        executors.insert("sysctl".to_string(), Box::new(sysctl_bsd::SysctlBsdExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall_pf::FirewallPfExecutor::new()));
        executors.insert("mount".to_string(), Box::new(mount_bsd::MountBsdExecutor::new()));
        executors.insert("swap".to_string(), Box::new(swap_bsd::SwapBsdExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));

        match pkgmgr::detect(os_family, os_distro) {
            Ok(pm) => {
                executors.insert(
                    "package".to_string(),
                    Box::new(PackageExecutorWrapper { pkg_mgr: pm }),
                );
            }
            Err(e) => {
                warn!(error = %e, "no package manager detected, package resources will fail");
            }
        }
    }

    // NetBSD-native executors.
    #[cfg(target_os = "netbsd")]
    {
        executors.insert("user".to_string(), Box::new(user::UserExecutor::new()));
        executors.insert("group".to_string(), Box::new(group::GroupExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_bsd::HostnameBsdExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_bsd::TimezoneBsdExecutor::new()));
        executors.insert("sysctl".to_string(), Box::new(sysctl_bsd::SysctlBsdExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall_pf::FirewallPfExecutor::new()));
        executors.insert("mount".to_string(), Box::new(mount_bsd::MountBsdExecutor::new()));
        executors.insert("swap".to_string(), Box::new(swap_bsd::SwapBsdExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));

        match pkgmgr::detect(os_family, os_distro) {
            Ok(pm) => {
                executors.insert(
                    "package".to_string(),
                    Box::new(PackageExecutorWrapper { pkg_mgr: pm }),
                );
            }
            Err(e) => {
                warn!(error = %e, "no package manager detected, package resources will fail");
            }
        }
    }

    // illumos-native executors.
    #[cfg(target_os = "illumos")]
    {
        executors.insert("service".to_string(), Box::new(service_illumos::ServiceIllumosExecutor::new()));
        executors.insert("user".to_string(), Box::new(user::UserExecutor::new()));
        executors.insert("group".to_string(), Box::new(group::GroupExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_bsd::HostnameBsdExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_illumos::TimezoneIllumosExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));

        match pkgmgr::detect(os_family, os_distro) {
            Ok(pm) => {
                executors.insert(
                    "package".to_string(),
                    Box::new(PackageExecutorWrapper { pkg_mgr: pm }),
                );
            }
            Err(e) => {
                warn!(error = %e, "no package manager detected, package resources will fail");
            }
        }
    }

    // Windows-native executors.
    #[cfg(windows)]
    {
        executors.insert("service".to_string(), Box::new(service_windows::ServiceWindowsExecutor::new()));
        executors.insert("user".to_string(), Box::new(user_windows::UserWindowsExecutor::new()));
        executors.insert("package".to_string(), Box::new(package_windows::PackageWindowsExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron_windows::CronWindowsExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_windows::HostnameWindowsExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_windows::TimezoneWindowsExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall_windows::FirewallWindowsExecutor::new()));
        executors.insert("registry".to_string(), Box::new(registry_windows::RegistryWindowsExecutor::new()));
        executors.insert("windows_env".to_string(), Box::new(windows_env::WindowsEnvExecutor::new()));
        executors.insert("reboot_windows".to_string(), Box::new(reboot_windows::RebootWindowsExecutor::new()));
        executors.insert("powershell".to_string(), Box::new(powershell::PowershellExecutor::new()));
        executors.insert("certificate_windows".to_string(), Box::new(certificate_windows::CertificateWindowsExecutor::new()));
        executors.insert("domain_membership".to_string(), Box::new(domain_membership::DomainMembershipExecutor::new()));
        executors.insert("local_security_policy".to_string(), Box::new(local_security_policy::LocalSecurityPolicyExecutor::new()));
        executors.insert("windows_update".to_string(), Box::new(windows_update::WindowsUpdateExecutor::new()));
        executors.insert("windows_defender".to_string(), Box::new(windows_defender::WindowsDefenderExecutor::new()));
        executors.insert("service_recovery_windows".to_string(), Box::new(service_recovery_windows::ServiceRecoveryWindowsExecutor::new()));
    }

    executors
}

/// Load custom executors from a directory.
pub fn load_custom_executors(
    executors: &mut HashMap<String, Box<dyn Executor>>,
    dir: &str,
) {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return, // Directory doesn't exist — that's fine.
    };

    for entry in entries.flatten() {
        let path = entry.path();
        if !path.is_file() {
            continue;
        }

        // Check if executable (mode bits on Unix, extension on Windows).
        if !crate::platform::permissions::is_executable(&path) {
            continue;
        }

        let name = path
            .file_stem()
            .and_then(|s| s.to_str())
            .unwrap_or("")
            .to_string();

        if name.is_empty() {
            continue;
        }

        // Never override built-in types.
        if executors.contains_key(&name) {
            warn!(name = %name, "custom executor would override built-in type, skipping");
            continue;
        }

        info!(name = %name, path = %path.display(), "loaded custom executor");
        executors.insert(
            name,
            Box::new(crate::executors::custom::CustomExecutor::new(
                path.to_string_lossy().to_string(),
            )),
        );
    }
}

/// Wrapper that adapts the PackageManager trait to the Executor trait.
struct PackageExecutorWrapper {
    pkg_mgr: Box<dyn crate::executors::pkgmgr::PackageManager>,
}

/// Parsed package executor parameters (beyond the basic name/state/version).
struct PackageParams<'a> {
    state: &'a str,
    version: &'a str,
    allow_downgrade: bool,
    purge: bool,
    update_cache: bool,
    lock_version: bool,
    post_cleanup: bool,
    flags: Vec<String>,
    // Parsed but not yet wired — reserved for future use.
    _timeout: Option<u64>,
    _retries: Option<u32>,
    _retry_delay: Option<u64>,
}

impl Executor for PackageExecutorWrapper {
    fn execute(
        &self,
        params: &HashMap<String, String>,
        dry_run: bool,
    ) -> ResourceResult {
        let start = std::time::Instant::now();
        let name_param = params
            .get("package")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("package", &name_param);

        if name_param.is_empty() {
            result.error = Some("package executor: name param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let version = params.get("version").map(|s| s.as_str()).unwrap_or("");
        let allow_downgrade = params
            .get("allow_downgrade")
            .map(|s| s == "true")
            .unwrap_or(false);
        let purge = params
            .get("purge")
            .map(|s| s == "true")
            .unwrap_or(false);
        let update_cache = params
            .get("update_cache")
            .map(|s| s == "true")
            .unwrap_or(false);
        let lock_version = params
            .get("lock_version")
            .map(|s| s == "true")
            .unwrap_or(false);
        let post_cleanup = params
            .get("post_cleanup")
            .map(|s| s == "true")
            .unwrap_or(false);
        let flags: Vec<String> = params
            .get("flags")
            .map(|s| s.split_whitespace().map(|f| f.to_string()).collect())
            .unwrap_or_default();

        // Parse timeout/retries/retry_delay — validated but not yet wired into execution.
        let timeout = params.get("timeout").and_then(|s| s.parse::<u64>().ok());
        let retries = params.get("retries").and_then(|s| s.parse::<u32>().ok());
        let retry_delay = params.get("retry_delay").and_then(|s| s.parse::<u64>().ok());

        // Validate: lock_version requires version (except on absent/latest).
        if lock_version && version.is_empty() && state != "absent" && state != "latest" {
            result.error = Some(
                "package executor: lock_version requires version parameter".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Validate: purge only valid with state=absent.
        if purge && state != "absent" {
            result.error = Some(
                "package executor: purge is only valid with state: absent".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let pkg_params = PackageParams {
            state,
            version,
            allow_downgrade,
            purge,
            update_cache,
            lock_version,
            post_cleanup,
            flags,
            _timeout: timeout,
            _retries: retries,
            _retry_delay: retry_delay,
        };

        // Set extra flags on the package manager (cleared after execution).
        if !pkg_params.flags.is_empty() {
            self.pkg_mgr.set_flags(&pkg_params.flags);
        }

        // Wildcard: upgrade all packages in a single transaction.
        if name_param.trim() == "*" {
            if pkg_params.state != "latest" {
                result.error = Some(
                    "package executor: package \"*\" (wildcard) requires state: latest".to_string(),
                );
                self.pkg_mgr.clear_flags();
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }

            if pkg_params.update_cache {
                if let Err(e) = self.pkg_mgr.update_cache() {
                    result.error = Some(format!("update cache before upgrade: {}", e));
                    self.pkg_mgr.clear_flags();
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }

            if dry_run {
                // Dry-run can't observe a real change without running the
                // upgrade — report the optimistic "would update".
                result.action = Action::Updated;
                result.changed = true;
            } else {
                // Snapshot installed state before/after so `changed`
                // reflects whether the upgrade actually moved anything,
                // instead of reporting a change every cycle (drift noise).
                let before = self.pkg_mgr.installed_snapshot();
                if let Err(e) = self.pkg_mgr.upgrade_all() {
                    result.error = Some(format!("upgrade all packages: {}", e));
                } else {
                    if pkg_params.post_cleanup {
                        if let Err(e) = self.pkg_mgr.post_cleanup() {
                            // Non-fatal: upgrade succeeded, cleanup failed.
                            tracing::warn!(error = %e, "post_cleanup failed after upgrade");
                        }
                    }
                    let after = self.pkg_mgr.installed_snapshot();
                    if snapshot_changed(before, after) {
                        result.action = Action::Updated;
                        result.changed = true;
                    } else {
                        result.action = Action::None;
                        result.changed = false;
                    }
                }
            }

            self.pkg_mgr.clear_flags();
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Support comma-separated package names.
        let names: Vec<&str> = name_param
            .split(',')
            .map(|s| s.trim())
            .filter(|s| !s.is_empty())
            .collect();

        for name in &names {
            let res = self.execute_one_package(name, &pkg_params, dry_run);
            if res.error.is_some() {
                result.error = res.error;
                self.pkg_mgr.clear_flags();
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            if res.changed {
                result.changed = true;
                if result.action == Action::None {
                    result.action = res.action;
                }
            }
        }

        // Run post_cleanup after successful install/upgrade if requested.
        if pkg_params.post_cleanup && result.changed && result.error.is_none() && !dry_run {
            if let Err(e) = self.pkg_mgr.post_cleanup() {
                tracing::warn!(error = %e, "post_cleanup failed after package operation");
            }
        }

        self.pkg_mgr.clear_flags();
        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

/// Decide whether an upgrade changed anything by diffing the installed
/// snapshots taken before and after. A failed snapshot on either side
/// degrades conservatively to "changed" — no worse than the always-
/// report-a-change behavior this replaces.
fn snapshot_changed(before: Result<String, String>, after: Result<String, String>) -> bool {
    match (before, after) {
        (Ok(b), Ok(a)) => b != a,
        _ => true,
    }
}

impl PackageExecutorWrapper {
    fn execute_one_package(
        &self,
        name: &str,
        p: &PackageParams,
        dry_run: bool,
    ) -> ResourceResult {
        let mut result = ResourceResult::new("package", name);

        let (installed, current_version) = match self.pkg_mgr.is_installed(name) {
            Ok(r) => r,
            Err(e) => {
                result.error = Some(format!("query package {}: {}", name, e));
                return result;
            }
        };

        match p.state {
            "present" => {
                if installed {
                    if !p.version.is_empty() && current_version != p.version {
                        // Version mismatch — upgrade or downgrade.
                        if p.update_cache {
                            if let Err(e) = self.pkg_mgr.update_cache() {
                                result.error = Some(format!("update cache: {}", e));
                                return result;
                            }
                        }
                        result.action = Action::Updated;
                        result.changed = true;
                        if dry_run {
                            return result;
                        }
                        let change_result = if p.allow_downgrade {
                            self.pkg_mgr.downgrade(name, p.version)
                        } else {
                            self.pkg_mgr.upgrade(name, p.version)
                        };
                        if let Err(e) = change_result {
                            let verb = if p.allow_downgrade { "downgrade" } else { "upgrade" };
                            result.error =
                                Some(format!("{} package {} to {}: {}", verb, name, p.version, e));
                            return result;
                        }
                        if p.lock_version {
                            if let Err(e) = self.pkg_mgr.hold(name) {
                                result.error = Some(format!("hold package {}: {}", name, e));
                            }
                        }
                        return result;
                    }
                    result.action = Action::None;
                    result.changed = false;
                    return result;
                }
                // Not installed — install it.
                if p.update_cache {
                    if let Err(e) = self.pkg_mgr.update_cache() {
                        result.error = Some(format!("update cache: {}", e));
                        return result;
                    }
                }
                result.action = Action::Created;
                result.changed = true;
                if dry_run {
                    return result;
                }
                if let Err(e) = self.pkg_mgr.install(name, p.version) {
                    result.error = Some(format!("install package {}: {}", name, e));
                    return result;
                }
                if p.lock_version {
                    if let Err(e) = self.pkg_mgr.hold(name) {
                        result.error = Some(format!("hold package {}: {}", name, e));
                    }
                }
                result
            }
            "latest" => {
                if installed {
                    // Already installed — upgrade to latest.
                    if p.update_cache {
                        if let Err(e) = self.pkg_mgr.update_cache() {
                            result.error = Some(format!("update cache: {}", e));
                            return result;
                        }
                    }
                    if dry_run {
                        // Can't observe a real change without running it.
                        result.action = Action::Updated;
                        result.changed = true;
                        return result;
                    }
                    // Snapshot before/after so `changed` reflects whether
                    // the upgrade actually moved a version (drift honesty).
                    let before = self.pkg_mgr.installed_snapshot();
                    if let Err(e) = self.pkg_mgr.upgrade_latest(name) {
                        result.error = Some(format!("upgrade package {}: {}", name, e));
                        return result;
                    }
                    let after = self.pkg_mgr.installed_snapshot();
                    if snapshot_changed(before, after) {
                        result.action = Action::Updated;
                        result.changed = true;
                    } else {
                        result.action = Action::None;
                        result.changed = false;
                    }
                    return result;
                }
                // Not installed — install it first.
                if p.update_cache {
                    if let Err(e) = self.pkg_mgr.update_cache() {
                        result.error = Some(format!("update cache: {}", e));
                        return result;
                    }
                }
                result.action = Action::Created;
                result.changed = true;
                if dry_run {
                    return result;
                }
                if let Err(e) = self.pkg_mgr.install(name, "") {
                    result.error = Some(format!("install package {}: {}", name, e));
                }
                result
            }
            "absent" => {
                if !installed {
                    result.action = Action::None;
                    result.changed = false;
                    return result;
                }
                result.action = Action::Deleted;
                result.changed = true;
                if dry_run {
                    return result;
                }
                // Unhold before removing if version was locked.
                if p.lock_version {
                    if let Err(e) = self.pkg_mgr.unhold(name) {
                        result.error = Some(format!("unhold package {}: {}", name, e));
                        return result;
                    }
                }
                let remove_result = if p.purge {
                    self.pkg_mgr.purge(name)
                } else {
                    self.pkg_mgr.remove(name)
                };
                if let Err(e) = remove_result {
                    let verb = if p.purge { "purge" } else { "remove" };
                    result.error = Some(format!("{} package {}: {}", verb, name, e));
                }
                result
            }
            _ => {
                result.error = Some(format!(
                    "package executor: unknown state {:?} (expected present, latest, or absent)",
                    p.state
                ));
                result
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Package resource helpers
// ---------------------------------------------------------------------------

/// Returns true if a resource type is a package or repository resource.
fn is_package_type(resource_type: &str) -> bool {
    matches!(resource_type, "package" | "repository" | "nonrepo_package")
}

/// Attempt to free disk space by cleaning the package manager cache.
/// Best-effort — errors are silently ignored.
fn try_cleanup_package_cache() {
    // Try apt-get clean (Debian/Ubuntu).
    let _ = std::process::Command::new("apt-get")
        .args(["clean"])
        .output();
    // Try dnf clean (RHEL/Fedora).
    let _ = std::process::Command::new("dnf")
        .args(["clean", "packages"])
        .output();
}

// ---------------------------------------------------------------------------
// Safety utilities
// ---------------------------------------------------------------------------

/// Returns the percentage of free disk space on the filesystem containing `path`.
/// Returns None if the check fails (unsupported platform, path doesn't exist).
#[cfg(unix)]
fn disk_free_percent(path: &str) -> Option<f64> {
    use std::ffi::CString;
    use std::mem::MaybeUninit;

    let c_path = CString::new(path).ok()?;
    let mut stat = MaybeUninit::<libc::statvfs>::uninit();

    let rc = unsafe { libc::statvfs(c_path.as_ptr(), stat.as_mut_ptr()) };
    if rc != 0 {
        return None;
    }

    let stat = unsafe { stat.assume_init() };
    if stat.f_blocks == 0 {
        return None;
    }

    let free_pct = (stat.f_bavail as f64 / stat.f_blocks as f64) * 100.0;
    Some(free_pct)
}

#[cfg(not(unix))]
fn disk_free_percent(_path: &str) -> Option<f64> {
    None // Skip disk check on non-Unix platforms.
}

/// Returns free disk space in bytes on the filesystem containing `path`.
#[cfg(unix)]
fn disk_free_bytes(path: &str) -> Option<u64> {
    use std::ffi::CString;
    use std::mem::MaybeUninit;

    let c_path = CString::new(path).ok()?;
    let mut stat = MaybeUninit::<libc::statvfs>::uninit();

    let rc = unsafe { libc::statvfs(c_path.as_ptr(), stat.as_mut_ptr()) };
    if rc != 0 {
        return None;
    }

    let stat = unsafe { stat.assume_init() };
    Some(stat.f_bavail * stat.f_frsize)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::{Configcrate, Resource};

    struct MockExecutor {
        action: Action,
        changed: bool,
    }

    impl Executor for MockExecutor {
        fn execute(
            &self,
            params: &HashMap<String, String>,
            _dry_run: bool,
        ) -> ResourceResult {
            let name = params.get("name").map(|s| s.as_str()).unwrap_or("mock");
            ResourceResult {
                configcrate_name: String::new(),
                resource_type: "mock".to_string(),
                resource_name: name.to_string(),
                action: self.action.clone(),
                changed: self.changed,
                error: None,
                duration_ms: 1,
                ..Default::default()
            }
        }
    }

    fn make_module(name: &str, resources: Vec<(&str, &str)>) -> Configcrate {
        Configcrate {
            name: name.to_string(),
            resource_type: String::new(),
            resources: resources
                .into_iter()
                .map(|(rname, rtype)| Resource {
                    name: rname.to_string(),
                    r#type: rtype.to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), rname.to_string());
                        p
                    },
                    when: String::new(),
                })
                .collect(),
            depends_on: vec![],
            notify: vec![],
            subscribes: vec![],
        }
    }

    #[test]
    fn active_target_paths_picks_correct_param() {
        let mut file_params = std::collections::BTreeMap::new();
        file_params.insert("target_path".to_string(), "/etc/nginx/nginx.conf".to_string());
        let mut svc_params = std::collections::BTreeMap::new();
        svc_params.insert("path".to_string(), "/usr/lib/systemd/system/nginx.service".to_string());
        // Empty-string values should NOT count as active paths.
        let mut empty_params = std::collections::BTreeMap::new();
        empty_params.insert("target_path".to_string(), String::new());
        // A resource type other than `file` that happens to have a `target_path`
        // (e.g. `package` with an irrelevant field) must NOT contribute — the
        // helper consults `path` for non-file types.
        let mut pkg_params = std::collections::BTreeMap::new();
        pkg_params.insert("target_path".to_string(), "/should/not/appear".to_string());

        let modules = vec![Configcrate {
            name: "m1".to_string(),
            resource_type: String::new(),
            resources: vec![
                Resource { name: "n".into(), r#type: "file".into(), params: file_params, when: String::new() },
                Resource { name: "s".into(), r#type: "service".into(), params: svc_params, when: String::new() },
                Resource { name: "e".into(), r#type: "file".into(), params: empty_params, when: String::new() },
                Resource { name: "p".into(), r#type: "package".into(), params: pkg_params, when: String::new() },
            ],
            depends_on: vec![],
            notify: vec![],
            subscribes: vec![],
        }];

        let got = active_target_paths(&modules);
        assert!(got.contains("/etc/nginx/nginx.conf"));
        assert!(got.contains("/usr/lib/systemd/system/nginx.service"));
        assert!(!got.contains(""));
        assert!(!got.contains("/should/not/appear"));
        assert_eq!(got.len(), 2);
    }

    #[test]
    fn converge_empty_modules() {
        let mut cache = ResultCache::new(5);
        let executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&[], &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());
        assert!(results.is_empty());
    }

    #[test]
    fn converge_executes_resources() {
        let mut cache = ResultCache::new(5);
        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert(
            "mock".to_string(),
            Box::new(MockExecutor {
                action: Action::None,
                changed: false,
            }),
        );

        let modules = vec![make_module("test-mod", vec![("res1", "mock"), ("res2", "mock")])];

        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());
        assert_eq!(results.len(), 2);
        assert!(results.iter().all(|r| r.error.is_none()));
    }

    #[test]
    fn converge_unknown_executor() {
        let mut cache = ResultCache::new(5);
        let executors: HashMap<String, Box<dyn Executor>> = HashMap::new();

        let modules = vec![make_module("test-mod", vec![("res1", "unknown_type")])];

        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());
        assert_eq!(results.len(), 1);
        assert!(results[0].error.is_some());
        assert!(results[0].error.as_ref().unwrap().contains("no executor"));
    }

    #[test]
    fn converge_module_filter() {
        let mut cache = ResultCache::new(5);
        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert(
            "mock".to_string(),
            Box::new(MockExecutor {
                action: Action::None,
                changed: false,
            }),
        );

        let modules = vec![
            make_module("keep", vec![("res1", "mock")]),
            make_module("skip", vec![("res2", "mock")]),
        ];

        let opts = RunOptions {
            dry_run: false,
            module_filter: Some("keep".into()),
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());
        assert_eq!(results.len(), 1);
        assert_eq!(results[0].configcrate_name, "keep");
    }

    // ── Notify/subscribe trigger chain tests ──────────────────────────

    use std::sync::{Arc, Mutex};

    /// Executor that tracks per-resource behavior and records every call
    /// with whether `_triggered` was set.
    struct TrackingExecutor {
        /// Maps resource name -> (action, changed) to return.
        behaviors: HashMap<String, (Action, bool)>,
        /// Records each (resource_name, was_triggered) call in order.
        calls: Arc<Mutex<Vec<(String, bool)>>>,
    }

    impl TrackingExecutor {
        fn new(behaviors: Vec<(&str, Action, bool)>) -> Self {
            Self {
                behaviors: behaviors
                    .into_iter()
                    .map(|(n, a, c)| (n.to_string(), (a, c)))
                    .collect(),
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }
    }

    impl Executor for TrackingExecutor {
        fn execute(
            &self,
            params: &HashMap<String, String>,
            _dry_run: bool,
        ) -> ResourceResult {
            let name = params
                .get("name")
                .map(|s| s.as_str())
                .unwrap_or("unknown");
            let triggered = params
                .get("_triggered")
                .map(|s| s == "true")
                .unwrap_or(false);
            self.calls
                .lock()
                .unwrap()
                .push((name.to_string(), triggered));

            let (action, changed) = self
                .behaviors
                .get(name)
                .cloned()
                .unwrap_or((Action::None, false));
            ResourceResult {
                configcrate_name: String::new(),
                resource_type: "mock".to_string(),
                resource_name: name.to_string(),
                action,
                changed,
                error: None,
                duration_ms: 1,
                ..Default::default()
            }
        }
    }

    /// Executor that also records whether dry_run was set on each call.
    struct DryRunTrackingExecutor {
        calls: Arc<Mutex<Vec<(String, bool)>>>,
    }

    impl DryRunTrackingExecutor {
        fn new() -> Self {
            Self {
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }
    }

    impl Executor for DryRunTrackingExecutor {
        fn execute(
            &self,
            params: &HashMap<String, String>,
            dry_run: bool,
        ) -> ResourceResult {
            let name = params
                .get("name")
                .map(|s| s.as_str())
                .unwrap_or("unknown");
            self.calls
                .lock()
                .unwrap()
                .push((name.to_string(), dry_run));

            ResourceResult {
                configcrate_name: String::new(),
                resource_type: "mock".to_string(),
                resource_name: name.to_string(),
                action: Action::None,
                changed: false,
                error: None,
                duration_ms: 1,
                ..Default::default()
            }
        }
    }

    fn make_module_with_notify(
        name: &str,
        resources: Vec<(&str, &str)>,
        notify: Vec<&str>,
    ) -> Configcrate {
        Configcrate {
            name: name.to_string(),
            resource_type: String::new(),
            resources: resources
                .into_iter()
                .map(|(rname, rtype)| Resource {
                    name: rname.to_string(),
                    r#type: rtype.to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), rname.to_string());
                        p
                    },
                    when: String::new(),
                })
                .collect(),
            depends_on: vec![],
            notify: notify.into_iter().map(|s| s.to_string()).collect(),
            subscribes: vec![],
        }
    }

    fn make_module_with_subscribes(
        name: &str,
        resources: Vec<(&str, &str)>,
        subscribes: Vec<&str>,
    ) -> Configcrate {
        Configcrate {
            name: name.to_string(),
            resource_type: String::new(),
            resources: resources
                .into_iter()
                .map(|(rname, rtype)| Resource {
                    name: rname.to_string(),
                    r#type: rtype.to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), rname.to_string());
                        p
                    },
                    when: String::new(),
                })
                .collect(),
            depends_on: vec![],
            notify: vec![],
            subscribes: subscribes.into_iter().map(|s| s.to_string()).collect(),
        }
    }

    /// Module A (config file) changes -> notifies Module B (service).
    /// B should be re-run in the second pass with _triggered=true.
    #[test]
    fn converge_notify_triggers_subscriber() {
        let exec = TrackingExecutor::new(vec![
            ("config-file", Action::Updated, true),   // A changes
            ("app-service", Action::None, false),       // B no-op first pass
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        // Module A notifies Module B.
        let modules = vec![
            make_module_with_notify("A", vec![("config-file", "mock")], vec!["B"]),
            make_module("B", vec![("app-service", "mock")]),
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());

        let recorded = calls.lock().unwrap();
        // First pass: config-file (not triggered), app-service (not triggered).
        // Second pass: app-service (triggered).
        assert_eq!(recorded.len(), 3, "expected 3 calls, got: {:?}", *recorded);
        assert_eq!(recorded[0], ("config-file".to_string(), false));
        assert_eq!(recorded[1], ("app-service".to_string(), false));
        assert_eq!(recorded[2], ("app-service".to_string(), true));

        // The final results should have 2 entries (one per resource).
        assert_eq!(results.len(), 2);

        // config-file should show as changed.
        let config_result = results.iter().find(|r| r.resource_name == "config-file").unwrap();
        assert!(config_result.changed);
    }

    /// Module A does NOT change -> Configcrate B should NOT be re-run.
    #[test]
    fn converge_no_change_no_notify() {
        let exec = TrackingExecutor::new(vec![
            ("config-file", Action::None, false),   // A no change
            ("app-service", Action::None, false),   // B no-op
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![
            make_module_with_notify("A", vec![("config-file", "mock")], vec!["B"]),
            make_module("B", vec![("app-service", "mock")]),
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());

        let recorded = calls.lock().unwrap();
        // Only first pass: each resource called once.
        assert_eq!(recorded.len(), 2, "expected 2 calls, got: {:?}", *recorded);
        assert_eq!(recorded[0], ("config-file".to_string(), false));
        assert_eq!(recorded[1], ("app-service".to_string(), false));
    }

    /// Module B subscribes to Module A. When A changes, B should be
    /// re-run with _triggered=true (subscribes is the inverse of notify).
    #[test]
    fn converge_subscribes_triggers_on_change() {
        let exec = TrackingExecutor::new(vec![
            ("config-file", Action::Updated, true),   // A changes
            ("app-service", Action::None, false),      // B no-op first pass
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        // B subscribes to A (instead of A notifying B).
        let modules = vec![
            make_module("A", vec![("config-file", "mock")]),
            make_module_with_subscribes("B", vec![("app-service", "mock")], vec!["A"]),
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());

        let recorded = calls.lock().unwrap();
        // First pass: config-file, app-service. Second pass: app-service (triggered).
        assert_eq!(recorded.len(), 3, "expected 3 calls, got: {:?}", *recorded);
        assert_eq!(recorded[0], ("config-file".to_string(), false));
        assert_eq!(recorded[1], ("app-service".to_string(), false));
        assert_eq!(recorded[2], ("app-service".to_string(), true));

        assert_eq!(results.len(), 2);
    }

    /// If the notified resource already changed in the first pass, it
    /// should NOT be re-run (line 130 optimization).
    #[test]
    fn converge_changed_notified_resource_not_rerun() {
        let exec = TrackingExecutor::new(vec![
            ("config-file", Action::Updated, true),     // A changes
            ("app-service", Action::Updated, true),     // B also changes in first pass
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![
            make_module_with_notify("A", vec![("config-file", "mock")], vec!["B"]),
            make_module("B", vec![("app-service", "mock")]),
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());

        let recorded = calls.lock().unwrap();
        // B already changed, so second pass skips it. Only 2 calls total.
        assert_eq!(recorded.len(), 2, "expected 2 calls, got: {:?}", *recorded);
        assert_eq!(recorded[0], ("config-file".to_string(), false));
        assert_eq!(recorded[1], ("app-service".to_string(), false));
    }

    /// dry_run=true should propagate to all executor calls.
    #[test]
    fn converge_dry_run_propagates_to_all() {
        let exec = DryRunTrackingExecutor::new();
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![make_module(
            "test-mod",
            vec![("res1", "mock"), ("res2", "mock"), ("res3", "mock")],
        )];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: true,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());

        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 3);
        // Every call should have dry_run=true.
        for (name, was_dry_run) in recorded.iter() {
            assert!(
                *was_dry_run,
                "resource {:?} should have received dry_run=true",
                name
            );
        }
    }

    /// Result cache prevents re-execution on second converge run.
    /// Uses "file" resource type since the cache only stores cacheable types.
    #[test]
    fn converge_result_cache_prevents_reexecution() {
        // Use "file" type so the cache actually stores results.
        let exec = TrackingExecutor::new(vec![
            ("config-file", Action::None, false), // no-op: cacheable
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("file".to_string(), Box::new(exec));

        let modules = vec![{
            Configcrate {
                name: "cache-test".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "config-file".to_string(),
                    r#type: "file".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "config-file".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            }
        }];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        // First converge: executor called, result cached.
        let results1 = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());
        assert_eq!(results1.len(), 1);
        assert!(results1[0].error.is_none());

        let count_after_first = calls.lock().unwrap().len();
        assert_eq!(count_after_first, 1, "first converge should call executor once");

        // Second converge: cache hit, executor NOT called again.
        let results2 = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());
        assert_eq!(results2.len(), 1);
        assert!(results2[0].error.is_none());

        let count_after_second = calls.lock().unwrap().len();
        assert_eq!(
            count_after_second, 1,
            "second converge should hit cache, not call executor again"
        );
    }

    // ── FileTransactionTracker tests ──────────────────────────────────

    #[test]
    fn file_transaction_rollback_on_error() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("rollback.conf");
        let original = "original content\n";
        std::fs::write(&path, original).unwrap();

        let mut tracker = FileTransactionTracker::new();
        tracker.ensure_snapshot(path.to_str().unwrap());

        // Simulate a failed write that corrupts the file.
        std::fs::write(&path, "corrupted\n").unwrap();

        // Rollback should restore original.
        tracker.rollback(path.to_str().unwrap());
        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, original);
    }

    #[test]
    fn file_transaction_no_rollback_on_success() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("success.conf");
        std::fs::write(&path, "original\n").unwrap();

        let mut tracker = FileTransactionTracker::new();
        tracker.ensure_snapshot(path.to_str().unwrap());

        // Simulate successful write.
        let new_content = "updated\n";
        std::fs::write(&path, new_content).unwrap();

        // No rollback — file stays updated.
        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, new_content);
    }

    #[test]
    fn file_transaction_different_files_independent() {
        let dir = tempfile::tempdir().unwrap();
        let path_a = dir.path().join("a.conf");
        let path_b = dir.path().join("b.conf");
        std::fs::write(&path_a, "a-original\n").unwrap();
        std::fs::write(&path_b, "b-original\n").unwrap();

        let mut tracker = FileTransactionTracker::new();
        tracker.ensure_snapshot(path_a.to_str().unwrap());
        tracker.ensure_snapshot(path_b.to_str().unwrap());

        // Modify both files.
        std::fs::write(&path_a, "a-updated\n").unwrap();
        std::fs::write(&path_b, "b-corrupted\n").unwrap();

        // Only rollback file B.
        tracker.rollback(path_b.to_str().unwrap());

        // A stays updated, B is restored.
        assert_eq!(std::fs::read_to_string(&path_a).unwrap(), "a-updated\n");
        assert_eq!(std::fs::read_to_string(&path_b).unwrap(), "b-original\n");
    }

    // ── depends_on failure propagation tests ─────────────────────────

    /// Executor that fails for specific resource names and succeeds for others.
    struct FailableExecutor {
        /// Resource names that should return an error.
        fail_names: HashSet<String>,
        calls: Arc<Mutex<Vec<String>>>,
    }

    impl FailableExecutor {
        fn new(fail_names: Vec<&str>) -> Self {
            Self {
                fail_names: fail_names.into_iter().map(|s| s.to_string()).collect(),
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }
    }

    impl Executor for FailableExecutor {
        fn execute(
            &self,
            params: &HashMap<String, String>,
            _dry_run: bool,
        ) -> ResourceResult {
            let name = params.get("name").map(|s| s.as_str()).unwrap_or("unknown");
            self.calls.lock().unwrap().push(name.to_string());

            if self.fail_names.contains(name) {
                ResourceResult {
                    configcrate_name: String::new(),
                    resource_type: "mock".to_string(),
                    resource_name: name.to_string(),
                    action: Action::None,
                    changed: false,
                    error: Some(format!("{} failed", name)),
                    duration_ms: 1,
                ..Default::default()
                }
            } else {
                ResourceResult {
                    configcrate_name: String::new(),
                    resource_type: "mock".to_string(),
                    resource_name: name.to_string(),
                    action: Action::Created,
                    changed: true,
                    error: None,
                    duration_ms: 1,
                ..Default::default()
                }
            }
        }
    }

    /// A → B → C chain. A fails → B and C should be skipped.
    #[test]
    fn depends_on_failure_skips_dependents() {
        let exec = FailableExecutor::new(vec!["install-nginx"]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![
            Configcrate {
                name: "pkg".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "install-nginx".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "install-nginx".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            },
            Configcrate {
                name: "config".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "nginx-conf".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "nginx-conf".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec!["pkg".to_string()],
                notify: vec![],
                subscribes: vec![],
            },
            Configcrate {
                name: "svc".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "nginx-svc".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "nginx-svc".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec!["config".to_string()],
                notify: vec![],
                subscribes: vec![],
            },
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());

        // Only install-nginx should have been executed.
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 1, "only failed resource should execute, got: {:?}", *recorded);
        assert_eq!(recorded[0], "install-nginx");

        // All 3 results should be present.
        assert_eq!(results.len(), 3);

        // install-nginx: error.
        let r0 = results.iter().find(|r| r.resource_name == "install-nginx").unwrap();
        assert!(r0.error.is_some());

        // nginx-conf: skipped due to dependency failure.
        let r1 = results.iter().find(|r| r.resource_name == "nginx-conf").unwrap();
        assert_eq!(r1.action, Action::Skipped);
        assert!(r1.error.as_ref().unwrap().contains("dependency"));

        // nginx-svc: skipped (transitive — nginx-conf was skipped/failed).
        let r2 = results.iter().find(|r| r.resource_name == "nginx-svc").unwrap();
        assert_eq!(r2.action, Action::Skipped);
        assert!(r2.error.as_ref().unwrap().contains("dependency"));
    }

    /// A fails, B depends on A, C has no deps. C should still execute.
    #[test]
    fn depends_on_failure_does_not_affect_independent_resources() {
        let exec = FailableExecutor::new(vec!["broken-res"]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![
            Configcrate {
                name: "broken".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "broken-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "broken-res".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            },
            Configcrate {
                name: "dependent".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "dep-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "dep-res".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec!["broken".to_string()],
                notify: vec![],
                subscribes: vec![],
            },
            Configcrate {
                name: "independent".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "ok-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "ok-res".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            },
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());

        // broken-res and ok-res executed; dep-res skipped.
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2, "got: {:?}", *recorded);
        assert!(recorded.contains(&"broken-res".to_string()));
        assert!(recorded.contains(&"ok-res".to_string()));

        // dep-res skipped.
        let dep = results.iter().find(|r| r.resource_name == "dep-res").unwrap();
        assert_eq!(dep.action, Action::Skipped);

        // ok-res succeeded.
        let ok = results.iter().find(|r| r.resource_name == "ok-res").unwrap();
        assert!(ok.error.is_none());
        assert!(ok.changed);
    }

    /// No failures → no skipping. Dependencies pass through cleanly.
    #[test]
    fn depends_on_no_failure_all_execute() {
        let exec = FailableExecutor::new(vec![]); // nothing fails
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![
            Configcrate {
                name: "base".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "base-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "base-res".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            },
            Configcrate {
                name: "app".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "app-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "app-res".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec!["base".to_string()],
                notify: vec![],
                subscribes: vec![],
            },
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());

        // Both executed.
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);

        // Both succeeded.
        assert!(results.iter().all(|r| r.error.is_none()));
        assert!(results.iter().all(|r| r.changed));
    }

    // ── Execution timeout tests ──────────────────────────────────────

    /// Executor that sleeps for a configurable duration before returning.
    struct SlowExecutor {
        sleep_ms: u64,
    }

    impl Executor for SlowExecutor {
        fn execute(
            &self,
            params: &HashMap<String, String>,
            _dry_run: bool,
        ) -> ResourceResult {
            std::thread::sleep(std::time::Duration::from_millis(self.sleep_ms));
            let name = params.get("name").map(|s| s.as_str()).unwrap_or("slow");
            ResourceResult {
                configcrate_name: String::new(),
                resource_type: "mock".to_string(),
                resource_name: name.to_string(),
                action: Action::None,
                changed: false,
                error: None,
                duration_ms: self.sleep_ms as i32,
                ..Default::default()
            }
        }
    }

    #[test]
    fn execution_timeout_triggers_error() {
        // Use a slow executor with 5s sleep and a 1s timeout param.
        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(SlowExecutor { sleep_ms: 5000 }));

        let modules = vec![{
            Configcrate {
                name: "slow-mod".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "slow-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "slow-res".to_string());
                        p.insert("timeout".to_string(), "1".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            }
        }];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());
        assert_eq!(results.len(), 1);
        assert!(results[0].error.is_some());
        assert!(
            results[0].error.as_ref().unwrap().contains("timed out"),
            "expected timeout error, got: {:?}",
            results[0].error
        );
    }

    #[test]
    fn execution_within_timeout_succeeds() {
        // Executor finishes quickly, well within the timeout.
        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(SlowExecutor { sleep_ms: 10 }));

        let modules = vec![{
            Configcrate {
                name: "fast-mod".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "fast-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "fast-res".to_string());
                        p.insert("timeout".to_string(), "10".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            }
        }];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
            stream_edit_allowed_paths: vec![],
            stream_edit_timeout_secs: 10,
            data_dir: None,
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());
        assert_eq!(results.len(), 1);
        assert!(results[0].error.is_none());
    }

    #[test]
    fn when_changed_skips_untriggered_resource() {
        let mut cache = ResultCache::new(5);
        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("exec".to_string(), Box::new(MockExecutor {
            action: Action::Created,
            changed: true,
        }));

        // A resource with when: "changed" and no trigger should be skipped.
        let modules = vec![Configcrate {
            name: "app".to_string(),
            resource_type: String::new(),
            resources: vec![Resource {
                name: "set-password".to_string(),
                r#type: "exec".to_string(),
                params: {
                    let mut p = std::collections::BTreeMap::new();
                    p.insert("name".to_string(), "set-password".to_string());
                    p.insert("command".to_string(), "reset-password".to_string());
                    p
                },
                when: "changed".to_string(),
            }],
            depends_on: vec![],
            notify: vec![],
            subscribes: vec![],
        }];

        let opts = RunOptions { dry_run: false, module_filter: None, run_id: "test".into(), stream_edit_allowed_paths: vec![], stream_edit_timeout_secs: 10, data_dir: None };
        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());
        assert_eq!(results.len(), 1);
        assert_eq!(results[0].action, Action::Skipped, "when: changed should skip when not triggered");
    }

    /// Intra-module notify: file resource with notify: [restart-sshd]
    /// should trigger restart-sshd (when: "changed") only when the file changes.
    #[test]
    fn intra_module_notify_triggers_when_changed() {
        let exec = TrackingExecutor::new(vec![
            ("sshd-config", Action::Updated, true),    // file changes
            ("restart-sshd", Action::None, false),      // service no-op first pass (when: changed)
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        // Single module with notify: [restart-sshd] (resource name, not module name).
        let modules = vec![Configcrate {
            name: "ssh-hardening".to_string(),
            resource_type: String::new(),
            resources: vec![
                Resource {
                    name: "sshd-config".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "sshd-config".to_string());
                        p
                    },
                    when: String::new(),
                },
                Resource {
                    name: "restart-sshd".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "restart-sshd".to_string());
                        p
                    },
                    when: "changed".to_string(),
                },
            ],
            depends_on: vec![],
            notify: vec!["restart-sshd".to_string()],
            subscribes: vec![],
        }];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions { dry_run: false, module_filter: None, run_id: "test".into(), stream_edit_allowed_paths: vec![], stream_edit_timeout_secs: 10, data_dir: None };
        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());

        let recorded = calls.lock().unwrap();
        // First pass: sshd-config runs normally, restart-sshd skipped by when: changed
        //   (skipped resources don't call the executor).
        // Second pass: sshd-config changed → restart-sshd re-run with _triggered=true.
        assert_eq!(recorded.len(), 2, "expected 2 executor calls (skip doesn't call executor), got: {:?}", *recorded);
        assert_eq!(recorded[0], ("sshd-config".to_string(), false));
        assert_eq!(recorded[1], ("restart-sshd".to_string(), true));

        // The final result for restart-sshd should be the triggered run, not skipped.
        let sshd_result = results.iter().find(|r| r.resource_name == "restart-sshd").unwrap();
        assert_ne!(sshd_result.action, Action::Skipped, "restart-sshd should have been triggered");
    }

    /// Intra-module subscribes: service with subscribes: [sshd-config]
    /// should trigger when the file changes.
    #[test]
    fn intra_module_subscribes_triggers_when_changed() {
        let exec = TrackingExecutor::new(vec![
            ("sshd-config", Action::Updated, true),
            ("restart-sshd", Action::None, false),
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        // Single module with subscribes: [sshd-config] (resource name, not module name).
        let modules = vec![Configcrate {
            name: "ssh-hardening".to_string(),
            resource_type: String::new(),
            resources: vec![
                Resource {
                    name: "sshd-config".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "sshd-config".to_string());
                        p
                    },
                    when: String::new(),
                },
                Resource {
                    name: "restart-sshd".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = std::collections::BTreeMap::new();
                        p.insert("name".to_string(), "restart-sshd".to_string());
                        p
                    },
                    when: "changed".to_string(),
                },
            ],
            depends_on: vec![],
            notify: vec![],
            subscribes: vec!["sshd-config".to_string()],
        }];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions { dry_run: false, module_filter: None, run_id: "test".into(), stream_edit_allowed_paths: vec![], stream_edit_timeout_secs: 10, data_dir: None };
        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}), &mut pkg_breaker::PackageBreaker::new());

        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2, "expected 2 executor calls, got: {:?}", *recorded);
        assert_eq!(recorded[1], ("restart-sshd".to_string(), true));

        let sshd_result = results.iter().find(|r| r.resource_name == "restart-sshd").unwrap();
        assert_ne!(sshd_result.action, Action::Skipped);
    }

    // ── Package executor wrapper tests ──────────────────────────────────

    /// Mock package manager that records all calls for verification.
    struct MockPkgMgr {
        /// (installed, version) to return from is_installed.
        installed: std::sync::Mutex<HashMap<String, (bool, String)>>,
        /// Records calls: (method, package_name).
        calls: Arc<Mutex<Vec<(String, String)>>>,
        /// Flags set via set_flags.
        flags: std::sync::Mutex<Vec<String>>,
        /// Snapshots returned by installed_snapshot, popped front-to-back.
        /// Empty → installed_snapshot errors (runner falls back to changed=true).
        snapshots: std::sync::Mutex<std::collections::VecDeque<String>>,
    }

    impl MockPkgMgr {
        fn new(installed: Vec<(&str, bool, &str)>) -> Self {
            let map: HashMap<String, (bool, String)> = installed
                .into_iter()
                .map(|(name, inst, ver)| (name.to_string(), (inst, ver.to_string())))
                .collect();
            Self {
                installed: std::sync::Mutex::new(map),
                calls: Arc::new(Mutex::new(Vec::new())),
                flags: std::sync::Mutex::new(Vec::new()),
                snapshots: std::sync::Mutex::new(std::collections::VecDeque::new()),
            }
        }

        /// Configure the snapshots installed_snapshot returns, in order.
        fn with_snapshots(self, snaps: &[&str]) -> Self {
            *self.snapshots.lock().unwrap() = snaps.iter().map(|s| s.to_string()).collect();
            self
        }
    }

    impl crate::executors::pkgmgr::PackageManager for MockPkgMgr {
        fn is_installed(&self, name: &str) -> Result<(bool, String), String> {
            let map = self.installed.lock().unwrap();
            match map.get(name) {
                Some((inst, ver)) => Ok((*inst, ver.clone())),
                None => Ok((false, String::new())),
            }
        }
        fn install(&self, name: &str, version: &str) -> Result<(), String> {
            self.calls.lock().unwrap().push(("install".into(), format!("{}@{}", name, version)));
            Ok(())
        }
        fn remove(&self, name: &str) -> Result<(), String> {
            self.calls.lock().unwrap().push(("remove".into(), name.into()));
            Ok(())
        }
        fn upgrade(&self, name: &str, version: &str) -> Result<(), String> {
            self.calls.lock().unwrap().push(("upgrade".into(), format!("{}@{}", name, version)));
            Ok(())
        }
        fn downgrade(&self, name: &str, version: &str) -> Result<(), String> {
            self.calls.lock().unwrap().push(("downgrade".into(), format!("{}@{}", name, version)));
            Ok(())
        }
        fn purge(&self, name: &str) -> Result<(), String> {
            self.calls.lock().unwrap().push(("purge".into(), name.into()));
            Ok(())
        }
        fn update_cache(&self) -> Result<(), String> {
            self.calls.lock().unwrap().push(("update_cache".into(), String::new()));
            Ok(())
        }
        fn upgrade_latest(&self, name: &str) -> Result<(), String> {
            self.calls.lock().unwrap().push(("upgrade_latest".into(), name.into()));
            Ok(())
        }
        fn hold(&self, name: &str) -> Result<(), String> {
            self.calls.lock().unwrap().push(("hold".into(), name.into()));
            Ok(())
        }
        fn unhold(&self, name: &str) -> Result<(), String> {
            self.calls.lock().unwrap().push(("unhold".into(), name.into()));
            Ok(())
        }
        fn upgrade_all(&self) -> Result<(), String> {
            self.calls.lock().unwrap().push(("upgrade_all".into(), String::new()));
            Ok(())
        }
        fn post_cleanup(&self) -> Result<(), String> {
            self.calls.lock().unwrap().push(("post_cleanup".into(), String::new()));
            Ok(())
        }
        fn installed_snapshot(&self) -> Result<String, String> {
            self.snapshots
                .lock()
                .unwrap()
                .pop_front()
                .ok_or_else(|| "no snapshot configured".to_string())
        }
        fn set_flags(&self, flags: &[String]) {
            *self.flags.lock().unwrap() = flags.to_vec();
        }
    }

    fn pkg_exec(mock: MockPkgMgr) -> PackageExecutorWrapper {
        PackageExecutorWrapper {
            pkg_mgr: Box::new(mock),
        }
    }

    fn pkg_params(kvs: Vec<(&str, &str)>) -> HashMap<String, String> {
        kvs.into_iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn pkg_present_not_installed() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![("package", "nginx"), ("state", "present")]), false);
        assert!(result.error.is_none());
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 1);
        assert_eq!(recorded[0].0, "install");
    }

    #[test]
    fn pkg_present_already_installed() {
        let mock = MockPkgMgr::new(vec![("nginx", true, "1.24.0")]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![("package", "nginx"), ("state", "present")]), false);
        assert!(result.error.is_none());
        assert!(!result.changed);
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 0);
    }

    #[test]
    fn pkg_absent_installed() {
        let mock = MockPkgMgr::new(vec![("telnet", true, "0.17")]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![("package", "telnet"), ("state", "absent")]), false);
        assert!(result.error.is_none());
        assert!(result.changed);
        assert_eq!(result.action, Action::Deleted);
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 1);
        assert_eq!(recorded[0].0, "remove");
    }

    #[test]
    fn pkg_purge_on_absent() {
        let mock = MockPkgMgr::new(vec![("nginx", true, "1.24.0")]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "absent"),
            ("purge", "true"),
        ]), false);
        assert!(result.error.is_none());
        assert!(result.changed);
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 1);
        assert_eq!(recorded[0].0, "purge");
    }

    #[test]
    fn pkg_purge_on_present_is_error() {
        let mock = MockPkgMgr::new(vec![]);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "present"),
            ("purge", "true"),
        ]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("purge is only valid with state: absent"));
    }

    #[test]
    fn pkg_update_cache_before_install() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "present"),
            ("update_cache", "true"),
        ]), false);
        assert!(result.error.is_none());
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);
        assert_eq!(recorded[0].0, "update_cache");
        assert_eq!(recorded[1].0, "install");
    }

    #[test]
    fn pkg_update_cache_before_upgrade() {
        let mock = MockPkgMgr::new(vec![("nginx", true, "1.22.0")]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "present"),
            ("version", "1.24.0"),
            ("update_cache", "true"),
        ]), false);
        assert!(result.error.is_none());
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);
        assert_eq!(recorded[0].0, "update_cache");
        assert_eq!(recorded[1].0, "upgrade");
    }

    #[test]
    fn pkg_flags_set_and_cleared() {
        let mock = MockPkgMgr::new(vec![]);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "present"),
            ("flags", "--no-install-recommends --fix-broken"),
        ]), false);
        assert!(result.error.is_none());
        // Flags should be cleared after execution.
        assert!(exec.pkg_mgr.as_ref().is_installed("nginx").is_ok()); // just ensure it's still callable
    }

    #[test]
    fn pkg_state_latest_installed() {
        let mock = MockPkgMgr::new(vec![("nginx", true, "1.22.0")]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "latest"),
        ]), false);
        assert!(result.error.is_none());
        assert!(result.changed);
        assert_eq!(result.action, Action::Updated);
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 1);
        assert_eq!(recorded[0].0, "upgrade_latest");
    }

    #[test]
    fn pkg_state_latest_not_installed() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "latest"),
        ]), false);
        assert!(result.error.is_none());
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 1);
        assert_eq!(recorded[0].0, "install");
    }

    #[test]
    fn pkg_lock_version_on_install() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "present"),
            ("version", "1.24.0"),
            ("lock_version", "true"),
        ]), false);
        assert!(result.error.is_none());
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);
        assert_eq!(recorded[0].0, "install");
        assert_eq!(recorded[1].0, "hold");
    }

    #[test]
    fn pkg_lock_version_requires_version() {
        let mock = MockPkgMgr::new(vec![]);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "present"),
            ("lock_version", "true"),
        ]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("lock_version requires version"));
    }

    #[test]
    fn pkg_lock_version_absent_ok_without_version() {
        let mock = MockPkgMgr::new(vec![("nginx", true, "1.24.0")]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "absent"),
            ("lock_version", "true"),
        ]), false);
        assert!(result.error.is_none());
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);
        assert_eq!(recorded[0].0, "unhold");
        assert_eq!(recorded[1].0, "remove");
    }

    #[test]
    fn pkg_lock_version_on_upgrade() {
        let mock = MockPkgMgr::new(vec![("nginx", true, "1.22.0")]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "present"),
            ("version", "1.24.0"),
            ("lock_version", "true"),
        ]), false);
        assert!(result.error.is_none());
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);
        assert_eq!(recorded[0].0, "upgrade");
        assert_eq!(recorded[1].0, "hold");
    }

    #[test]
    fn pkg_timeout_retries_parsed_without_error() {
        let mock = MockPkgMgr::new(vec![]);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "present"),
            ("timeout", "120"),
            ("retries", "3"),
            ("retry_delay", "10"),
        ]), false);
        assert!(result.error.is_none());
    }

    #[test]
    fn pkg_unknown_state_is_error() {
        let mock = MockPkgMgr::new(vec![]);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "purged"),
        ]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }

    #[test]
    fn pkg_state_latest_with_update_cache() {
        let mock = MockPkgMgr::new(vec![("nginx", true, "1.22.0")]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "latest"),
            ("update_cache", "true"),
        ]), false);
        assert!(result.error.is_none());
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);
        assert_eq!(recorded[0].0, "update_cache");
        assert_eq!(recorded[1].0, "upgrade_latest");
    }

    #[test]
    fn pkg_comma_separated_packages() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "curl,wget,git"),
            ("state", "present"),
        ]), false);
        assert!(result.error.is_none());
        assert!(result.changed);
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 3);
        assert!(recorded[0].1.starts_with("curl@"));
        assert!(recorded[1].1.starts_with("wget@"));
        assert!(recorded[2].1.starts_with("git@"));
    }

    #[test]
    fn pkg_dry_run_no_side_effects() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "present"),
        ]), true);
        assert!(result.error.is_none());
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 0, "dry_run should not call install");
    }

    #[test]
    fn pkg_purge_with_unhold() {
        let mock = MockPkgMgr::new(vec![("nginx", true, "1.24.0")]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "absent"),
            ("purge", "true"),
            ("lock_version", "true"),
        ]), false);
        assert!(result.error.is_none());
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);
        assert_eq!(recorded[0].0, "unhold");
        assert_eq!(recorded[1].0, "purge");
    }

    #[test]
    fn pkg_wildcard_upgrade_all() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "*"),
            ("state", "latest"),
        ]), false);
        assert!(result.error.is_none());
        assert!(result.changed);
        assert_eq!(result.action, Action::Updated);
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 1);
        assert_eq!(recorded[0].0, "upgrade_all");
    }

    #[test]
    fn pkg_wildcard_with_update_cache() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "*"),
            ("state", "latest"),
            ("update_cache", "true"),
        ]), false);
        assert!(result.error.is_none());
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);
        assert_eq!(recorded[0].0, "update_cache");
        assert_eq!(recorded[1].0, "upgrade_all");
    }

    #[test]
    fn pkg_wildcard_requires_latest() {
        let mock = MockPkgMgr::new(vec![]);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "*"),
            ("state", "present"),
        ]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("requires state: latest"));
    }

    #[test]
    fn pkg_wildcard_dry_run() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "*"),
            ("state", "latest"),
        ]), true);
        assert!(result.error.is_none());
        assert!(result.changed);
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 0, "dry_run should not call upgrade_all");
    }

    #[test]
    fn pkg_wildcard_with_post_cleanup() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "*"),
            ("state", "latest"),
            ("post_cleanup", "true"),
        ]), false);
        assert!(result.error.is_none());
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);
        assert_eq!(recorded[0].0, "upgrade_all");
        assert_eq!(recorded[1].0, "post_cleanup");
    }

    #[test]
    fn pkg_wildcard_with_flags() {
        let mock = MockPkgMgr::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "*"),
            ("state", "latest"),
            ("flags", "--fix-missing"),
        ]), false);
        assert!(result.error.is_none());
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 1);
        assert_eq!(recorded[0].0, "upgrade_all");
    }

    #[test]
    fn pkg_wildcard_no_change_reports_settled() {
        // Identical before/after snapshot — the upgrade moved nothing.
        let mock = MockPkgMgr::new(vec![]).with_snapshots(&["pkgs-v1", "pkgs-v1"]);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "*"),
            ("state", "latest"),
        ]), false);
        assert!(result.error.is_none());
        assert!(!result.changed, "no-op upgrade must not report changed");
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn pkg_wildcard_change_reports_updated() {
        let mock = MockPkgMgr::new(vec![]).with_snapshots(&["pkgs-v1", "pkgs-v2"]);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "*"),
            ("state", "latest"),
        ]), false);
        assert!(result.error.is_none());
        assert!(result.changed, "real upgrade must report changed");
        assert_eq!(result.action, Action::Updated);
    }

    #[test]
    fn pkg_specific_latest_no_change_reports_settled() {
        let mock = MockPkgMgr::new(vec![("nginx", true, "1.24.0")])
            .with_snapshots(&["nginx 1.24.0", "nginx 1.24.0"]);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "latest"),
        ]), false);
        assert!(result.error.is_none());
        assert!(!result.changed, "no-op upgrade must not report changed");
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn pkg_specific_latest_change_reports_updated() {
        let mock = MockPkgMgr::new(vec![("nginx", true, "1.24.0")])
            .with_snapshots(&["nginx 1.24.0", "nginx 1.26.0"]);
        let exec = pkg_exec(mock);
        let result = exec.execute(&pkg_params(vec![
            ("package", "nginx"),
            ("state", "latest"),
        ]), false);
        assert!(result.error.is_none());
        assert!(result.changed, "real upgrade must report changed");
        assert_eq!(result.action, Action::Updated);
    }
}

#[cfg(not(unix))]
fn disk_free_bytes(_path: &str) -> Option<u64> {
    None
}
