pub mod cron;
pub mod dag;

use anyhow::{Context, Result};
use chrono::Utc;
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, Instant};

use crate::capability::{self, CapabilityContext, CapabilityKind};
use crate::config::ConnwaerConfig;
use crate::results;
use crate::trends::TrendStore;
use cron::CronSchedule;

/// Tracks when each capability last ran (for interval schedules and dedup).
struct CapabilityState {
    kind: CapabilityKind,
    schedule: CronSchedule,
    config: capability::CapabilityConfig,
    last_run: Option<Instant>,
}

/// Run the scheduler loop. Evaluates cron/interval schedules every 60 seconds,
/// respects depends_on ordering, spawns capabilities with timeout.
pub async fn run(
    cfg: &ConnwaerConfig,
    trends: Arc<TrendStore>,
    version: &str,
    shutdown: tokio::sync::watch::Receiver<bool>,
) -> Result<()> {
    let hostname = crate::platform::os_info::hostname();
    let platform = crate::platform::os_info::platform_name().to_string();
    let results_dir = PathBuf::from(&cfg.results_dir);

    // Build execution order from depends_on.
    let dep_map: HashMap<String, Vec<String>> = cfg.capabilities.iter()
        .filter(|(_, e)| e.enabled)
        .map(|(name, entry)| (name.clone(), entry.depends_on.clone()))
        .collect();

    let order = dag::topological_sort(&dep_map)
        .context("resolving capability dependencies")?;

    // Build capability state.
    let mut caps: Vec<CapabilityState> = Vec::new();
    for name in &order {
        let entry = &cfg.capabilities[name];
        let kind = match CapabilityKind::from_name(name) {
            Some(k) => k,
            None => {
                tracing::warn!("unknown capability: {name}, skipping");
                continue;
            }
        };
        let inst = kind.instantiate();
        if !inst.supported() {
            tracing::info!("{name}: not supported on this platform");
            continue;
        }
        let schedule = CronSchedule::parse(&entry.schedule)
            .with_context(|| format!("parsing schedule for {name}"))?;

        caps.push(CapabilityState {
            kind,
            schedule,
            config: capability::CapabilityConfig {
                enabled: entry.enabled,
                params: entry.params.clone(),
                thresholds: entry.thresholds.clone(),
            },
            last_run: None,
        });
    }

    tracing::info!("scheduler started: {} capabilities in order: {}",
        caps.len(),
        order.join(" → "),
    );

    let mut shutdown = shutdown;

    loop {
        // Check for shutdown signal.
        if *shutdown.borrow() {
            tracing::info!("scheduler received shutdown signal");
            break;
        }

        let now_utc = Utc::now().naive_utc();

        for cap_state in &mut caps {
            let should_run = match &cap_state.schedule {
                CronSchedule::Cron { .. } => cap_state.schedule.matches(&now_utc),
                CronSchedule::Interval { seconds } => {
                    match cap_state.last_run {
                        None => true,
                        Some(last) => last.elapsed() >= Duration::from_secs(*seconds),
                    }
                }
            };

            if !should_run {
                continue;
            }

            let name = cap_state.kind.name();
            let kind = cap_state.kind;
            let inst = kind.instantiate();
            let timeout = inst.timeout();
            let controls: Vec<String> = inst.controls().iter().map(|s| s.to_string()).collect();

            let ctx = CapabilityContext {
                config: cap_state.config.clone(),
                results_dir: results_dir.clone(),
                trends_db: Arc::clone(&trends),
                hostname: hostname.clone(),
                platform: platform.clone(),
                version: version.to_string(),
            };

            tracing::info!("{name}: running (timeout: {}s)", timeout.as_secs());
            let start = Instant::now();

            // Run with timeout. inst is moved into the closure.
            let result = tokio::time::timeout(
                timeout,
                tokio::task::spawn_blocking(move || inst.verify(&ctx)),
            ).await;

            let elapsed = start.elapsed();
            cap_state.last_run = Some(Instant::now());

            match result {
                Ok(Ok(Ok(cap_result))) => {
                    tracing::info!("{name}: {} ({}ms)", cap_result.status, cap_result.duration_ms);
                    if let Err(e) = results::write_result(&results_dir, &cap_result) {
                        tracing::error!("{name}: failed to write result: {e:#}");
                    }
                    let metrics = serde_json::json!({
                        "duration_ms": cap_result.duration_ms,
                        "status": cap_result.status.to_string(),
                    });
                    let _ = trends.record_run(name, &cap_result.status.to_string(), &metrics);
                }
                Ok(Ok(Err(e))) => {
                    tracing::error!("{name}: verification error: {e:#}");
                    write_error_result(name, &e, elapsed, &results_dir, &hostname, &platform, version, &controls);
                }
                Ok(Err(e)) => {
                    tracing::error!("{name}: task panicked: {e}");
                    let err = anyhow::anyhow!("task panicked: {e}");
                    write_error_result(name, &err, elapsed, &results_dir, &hostname, &platform, version, &controls);
                }
                Err(_) => {
                    tracing::error!("{name}: timed out after {}s", timeout.as_secs());
                    let err = anyhow::anyhow!("timed out after {}s", timeout.as_secs());
                    write_error_result(name, &err, elapsed, &results_dir, &hostname, &platform, version, &controls);
                }
            }
        }

        // Sleep until next minute boundary (for cron) or 30 seconds (for intervals).
        tokio::select! {
            _ = tokio::time::sleep(Duration::from_secs(30)) => {}
            _ = shutdown.changed() => {
                if *shutdown.borrow() {
                    tracing::info!("scheduler shutting down");
                    break;
                }
            }
        }
    }

    Ok(())
}

fn write_error_result(
    name: &str,
    err: &anyhow::Error,
    elapsed: Duration,
    results_dir: &PathBuf,
    hostname: &str,
    platform: &str,
    version: &str,
    controls: &[String],
) {
    let error_result = capability::CapabilityResult {
        capability: name.to_string(),
        status: capability::VerificationStatus::Error,
        summary: format!("Error: {err}"),
        evidence: vec![],
        controls: controls.to_vec(),
        started_at: Utc::now().to_rfc3339(),
        completed_at: Utc::now().to_rfc3339(),
        duration_ms: elapsed.as_millis() as u64,
        platform: platform.to_string(),
        hostname: hostname.to_string(),
        version: version.to_string(),
        error: Some(format!("{err:#}")),
    };
    let _ = results::write_result(results_dir, &error_result);
}
