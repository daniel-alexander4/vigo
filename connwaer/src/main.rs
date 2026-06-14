use anyhow::{Context, Result};
use std::path::PathBuf;
use std::sync::Arc;

mod alerting;
mod bootstrap;
mod capability;
mod config;
mod license;
mod platform;
mod results;
mod rng;
mod scheduler;
mod trends;

use capability::{CapabilityContext, CapabilityKind};
use config::ConnwaerConfig;
use platform::{os_info, paths};
use trends::TrendStore;

const VERSION: &str = env!("CARGO_PKG_VERSION");

fn main() {
    let args: Vec<String> = std::env::args().collect();

    let mut config_path = paths::DEFAULT_CONFIG_PATH.to_string();
    let mut log_level = String::new();
    let mut i = 1;

    // Parse global flags before subcommand.
    while i < args.len() {
        match args[i].as_str() {
            "--config" | "-c" => {
                i += 1;
                if i < args.len() {
                    config_path = args[i].clone();
                }
            }
            "--log-level" => {
                i += 1;
                if i < args.len() {
                    log_level = args[i].clone();
                }
            }
            _ => break,
        }
        i += 1;
    }

    let subcommand = if i < args.len() { args[i].as_str() } else { "run" };
    let sub_args = if i + 1 < args.len() { &args[i + 1..] } else { &[] };

    match subcommand {
        "version" => {
            println!("connwaer {}", VERSION);
        }
        "status" => {
            if let Err(e) = cmd_status(&config_path) {
                eprintln!("error: {e:#}");
                std::process::exit(1);
            }
        }
        "run" => {
            let once = sub_args.contains(&"--once".to_string());
            if let Err(e) = cmd_run(&config_path, &log_level, once) {
                eprintln!("error: {e:#}");
                std::process::exit(1);
            }
        }
        "once" => {
            if let Err(e) = cmd_run(&config_path, &log_level, true) {
                eprintln!("error: {e:#}");
                std::process::exit(1);
            }
        }
        "verify" => {
            if sub_args.is_empty() {
                eprintln!("usage: connwaer verify <capability>");
                eprintln!("\ncapabilities:");
                for kind in CapabilityKind::all() {
                    eprintln!("  {}", kind.name());
                }
                std::process::exit(1);
            }
            if let Err(e) = cmd_verify(&config_path, &log_level, &sub_args[0]) {
                eprintln!("error: {e:#}");
                std::process::exit(1);
            }
        }
        "install-service" => {
            if let Err(e) = cmd_install_service() {
                eprintln!("error: {e:#}");
                std::process::exit(1);
            }
        }
        "help" | "--help" | "-h" => {
            print_usage();
        }
        other => {
            eprintln!("unknown command: {other}");
            print_usage();
            std::process::exit(1);
        }
    }
}

fn print_usage() {
    println!(
        "connwaer {} — active compliance verification agent

USAGE:
    connwaer [FLAGS] [COMMAND]

FLAGS:
    --config, -c <path>    Config file (default: {})
    --log-level <level>    Log level: trace, debug, info, warn, error

COMMANDS:
    run [--once]           Start daemon (default). --once exits after one cycle.
    once                   Run all capabilities once and exit.
    verify <capability>    Run a single capability and exit.
    status                 Show last result per capability.
    install-service        Install as system service.
    version                Print version.
    help                   Print this help.",
        VERSION,
        paths::DEFAULT_CONFIG_PATH,
    );
}

fn init_tracing(cfg_level: &str, cli_level: &str) {
    let level = if !cli_level.is_empty() {
        cli_level
    } else if !cfg_level.is_empty() {
        cfg_level
    } else {
        "info"
    };

    let filter = tracing_subscriber::EnvFilter::try_new(level)
        .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info"));

    tracing_subscriber::fmt()
        .with_env_filter(filter)
        .with_target(false)
        .init();
}

fn cmd_run(config_path: &str, cli_log_level: &str, once: bool) -> Result<()> {
    let cfg = ConnwaerConfig::load(std::path::Path::new(config_path))
        .with_context(|| format!("loading config from {config_path}"))?;

    init_tracing(&cfg.log_level, cli_log_level);
    tracing::info!("connwaer {} starting", VERSION);

    // Apply resource guardrails before any capability work.
    platform::resource::apply(&cfg.resource_limits);

    // License check (warn but don't block — allows evaluation without license).
    let license_dir = std::path::Path::new(&cfg.license_dir);
    match license::License::load_dir(license_dir) {
        Ok(lic) => {
            if lic.is_expired() {
                tracing::warn!("license {} is expired ({})", lic.payload.license_id, lic.payload.expires_at);
            }
            if !lic.has_feature("connwaer") {
                tracing::warn!(
                    "license {} does not include the 'connwaer' feature — running in evaluation mode",
                    lic.payload.license_id,
                );
            } else {
                tracing::info!("license valid: {} ({})", lic.payload.license_id, lic.payload.tier);
            }
        }
        Err(e) => {
            tracing::warn!("no valid license found: {e} — running in evaluation mode");
        }
    }

    let trends = Arc::new(
        TrendStore::open(std::path::Path::new(&cfg.trends_db))
            .context("opening trends database")?,
    );

    if once {
        // Single cycle: run all enabled capabilities in order, then exit.
        run_once(&cfg, &trends)?;
        return Ok(());
    }

    // Daemon mode: run the scheduler loop with signal handling.
    let rt = tokio::runtime::Runtime::new()?;
    rt.block_on(async {
        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);

        // Handle SIGINT/SIGTERM for graceful shutdown.
        let tx = shutdown_tx.clone();
        tokio::spawn(async move {
            let _ = tokio::signal::ctrl_c().await;
            tracing::info!("received shutdown signal");
            let _ = tx.send(true);
        });

        scheduler::run(&cfg, trends, VERSION, shutdown_rx).await
    })?;

    Ok(())
}

/// Run all enabled capabilities once (no scheduling, no daemon).
fn run_once(cfg: &ConnwaerConfig, trends: &Arc<TrendStore>) -> Result<()> {
    let results_dir = PathBuf::from(&cfg.results_dir);
    let hostname = os_info::hostname();
    let platform_name = os_info::platform_name().to_string();

    // Fire alerts on failures even in single-cycle mode. Dedup/recovery state
    // doesn't carry between invocations here, but a Fail/Error still notifies.
    let mut alerts = alerting::AlertDispatcher::new(cfg.alerting.clone());

    // Resolve execution order.
    let dep_map: std::collections::HashMap<String, Vec<String>> = cfg.capabilities.iter()
        .filter(|(_, e)| e.enabled)
        .map(|(name, entry)| (name.clone(), entry.depends_on.clone()))
        .collect();

    let order = scheduler::dag::topological_sort(&dep_map)
        .context("resolving capability dependencies")?;

    let mut count = 0;
    for name in &order {
        let entry = &cfg.capabilities[name];
        let kind = match CapabilityKind::from_name(name) {
            Some(k) => k,
            None => {
                tracing::info!("unknown capability: {name}, skipping");
                continue;
            }
        };
        let inst = kind.instantiate();
        if !inst.supported() {
            tracing::info!("{name}: not supported on this platform");
            continue;
        }

        let ctx = CapabilityContext {
            config: capability::CapabilityConfig {
                enabled: entry.enabled,
                params: entry.params.clone(),
                thresholds: entry.thresholds.clone(),
            },
            results_dir: results_dir.clone(),
            trends_db: Arc::clone(trends),
            hostname: hostname.clone(),
            platform: platform_name.clone(),
            version: VERSION.to_string(),
        };

        tracing::info!("{name}: running");
        let start = std::time::Instant::now();

        match inst.verify(&ctx) {
            Ok(result) => {
                tracing::info!("{name}: {} ({}ms)", result.status, result.duration_ms);
                if let Err(e) = results::write_result(&results_dir, &result) {
                    tracing::error!("{name}: failed to write result: {e:#}");
                }
                let metrics = serde_json::json!({
                    "duration_ms": result.duration_ms,
                    "status": result.status.to_string(),
                });
                let _ = trends.record_run(name, &result.status.to_string(), &metrics);
                alerts.evaluate(&result);
            }
            Err(e) => {
                let elapsed = start.elapsed();
                tracing::error!("{name}: error: {e:#}");
                let error_result = capability::CapabilityResult {
                    capability: name.to_string(),
                    status: capability::VerificationStatus::Error,
                    summary: format!("Error: {e}"),
                    evidence: vec![],
                    controls: inst.controls().iter().map(|s| s.to_string()).collect(),
                    started_at: chrono::Utc::now().to_rfc3339(),
                    completed_at: chrono::Utc::now().to_rfc3339(),
                    duration_ms: elapsed.as_millis() as u64,
                    platform: platform_name.clone(),
                    hostname: hostname.clone(),
                    version: VERSION.to_string(),
                    error: Some(format!("{e:#}")),
                };
                let _ = results::write_result(&results_dir, &error_result);
                alerts.evaluate(&error_result);
            }
        }
        count += 1;
    }

    tracing::info!("single cycle complete: {count} capabilities run");
    Ok(())
}

fn cmd_verify(config_path: &str, cli_log_level: &str, cap_name: &str) -> Result<()> {
    let kind = CapabilityKind::from_name(cap_name)
        .with_context(|| format!("unknown capability: {cap_name}"))?;

    let cfg = ConnwaerConfig::load(std::path::Path::new(config_path))
        .with_context(|| format!("loading config from {config_path}"))?;

    init_tracing(&cfg.log_level, cli_log_level);

    let trends = Arc::new(TrendStore::open_memory()?);
    let results_dir = PathBuf::from(&cfg.results_dir);

    let cap_config = cfg.capabilities.get(cap_name)
        .map(|e| capability::CapabilityConfig {
            enabled: e.enabled,
            params: e.params.clone(),
            thresholds: e.thresholds.clone(),
        })
        .unwrap_or_default();

    let ctx = CapabilityContext {
        config: cap_config,
        results_dir: results_dir.clone(),
        trends_db: trends,
        hostname: os_info::hostname(),
        platform: os_info::platform_name().to_string(),
        version: VERSION.to_string(),
    };

    let cap = kind.instantiate();
    let result = cap.verify(&ctx)?;
    let json = serde_json::to_string_pretty(&result)?;
    println!("{json}");

    results::write_result(&results_dir, &result)?;
    Ok(())
}

fn cmd_status(config_path: &str) -> Result<()> {
    let cfg = ConnwaerConfig::load(std::path::Path::new(config_path))
        .with_context(|| format!("loading config from {config_path}"))?;

    let results_dir = PathBuf::from(&cfg.results_dir);

    if !results_dir.exists() {
        println!("No results directory found at {}", results_dir.display());
        return Ok(());
    }

    let mut found = false;
    for entry in std::fs::read_dir(&results_dir)? {
        let entry = entry?;
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) != Some("json") {
            continue;
        }
        let content = std::fs::read_to_string(&path)?;
        if let Ok(result) = serde_json::from_str::<capability::CapabilityResult>(&content) {
            found = true;
            println!(
                "{:<25} {:<8} {}  ({})",
                result.capability, result.status, result.completed_at, result.summary
            );
        }
    }

    if !found {
        println!("No results found in {}", results_dir.display());
    }

    Ok(())
}

fn cmd_install_service() -> Result<()> {
    bootstrap::install_service()
}
