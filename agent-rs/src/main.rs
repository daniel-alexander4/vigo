// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

use hobnob::auth;
use hobnob::bootstrap;
use hobnob::checkin;
use hobnob::config;
use hobnob::executors;
use hobnob::platform;
use hobnob::policy;
use hobnob::update;
use hobnob::proto;
use hobnob::runner;
use hobnob::snapshots;
use hobnob::state;
use hobnob::traits;

use anyhow::{bail, Context, Result};
use std::collections::HashSet;
use std::path::PathBuf;
use tracing::{info, warn};

/// Version injected at build time.
const VERSION: &str = env!("CARGO_PKG_VERSION");

struct Cli {
    data_dir: PathBuf,
    server: String,
    ca_file: Option<String>,
    cert_file: Option<String>,
    key_file: Option<String>,
    log_level: String,
    api_addr: String,
    executors_dir: String,
    snapshot_dir: String,
    snapshot_retention: usize,
    trait_refresh_cycles: usize,
    result_cache_cycles: usize,
    dry_run: bool,
    once: bool,
    command: Option<Commands>,
}

enum Commands {
    Run {
        module: Option<String>,
        dry_run: bool,
        no_daemon: bool,
    },
    Register {
        token: String,
        force: bool,
    },
    Bootstrap {
        server: String,
        token: Option<String>,
        install_service: bool,
        grpc_port: String,
        force: bool,
    },
    Version,
    Status,
    Traits {
        collectors: Vec<String>,
    },
}

fn parse_args() -> Result<Cli> {
    let mut cli = Cli {
        data_dir: platform::paths::default_data_dir(),
        server: "localhost:1530".into(),
        ca_file: None,
        cert_file: None,
        key_file: None,
        log_level: "info".into(),
        api_addr: "https://localhost:8443".into(),
        executors_dir: platform::paths::default_executors_dir().to_string_lossy().into(),
        snapshot_dir: platform::paths::default_snapshot_dir().to_string_lossy().into(),
        snapshot_retention: 5,
        trait_refresh_cycles: 10,
        result_cache_cycles: 5,
        dry_run: false,
        once: false,
        command: None,
    };

    let args: Vec<String> = std::env::args().skip(1).collect();
    let mut i = 0;
    let mut positional = Vec::new();
    let mut in_subcommand = false;
    let mut explicit = HashSet::new();

    while i < args.len() {
        // Once we hit a subcommand, all remaining args belong to it.
        if in_subcommand {
            positional.push(args[i].clone());
            i += 1;
            continue;
        }
        match args[i].as_str() {
            "--help" | "-h" => {
                print_usage();
                std::process::exit(0);
            }
            "--data-dir" => { i += 1; cli.data_dir = PathBuf::from(arg_val(&args, i)?); explicit.insert("data_dir"); }
            "--server" => { i += 1; cli.server = arg_val(&args, i)?; explicit.insert("server"); }
            "--ca-file" => { i += 1; cli.ca_file = Some(arg_val(&args, i)?); explicit.insert("ca_file"); }
            "--cert-file" => { i += 1; cli.cert_file = Some(arg_val(&args, i)?); explicit.insert("cert_file"); }
            "--key-file" => { i += 1; cli.key_file = Some(arg_val(&args, i)?); explicit.insert("key_file"); }
            "--log-level" => { i += 1; cli.log_level = arg_val(&args, i)?; explicit.insert("log_level"); }
            "--api-addr" => { i += 1; cli.api_addr = arg_val(&args, i)?; explicit.insert("api_addr"); }
            "--executors-dir" => { i += 1; cli.executors_dir = arg_val(&args, i)?; explicit.insert("executors_dir"); }
            "--snapshot-dir" => { i += 1; cli.snapshot_dir = arg_val(&args, i)?; explicit.insert("snapshot_dir"); }
            "--snapshot-retention" => { i += 1; cli.snapshot_retention = arg_val(&args, i)?.parse().context("--snapshot-retention must be a number")?; explicit.insert("snapshot_retention"); }
            "--trait-refresh-cycles" => { i += 1; cli.trait_refresh_cycles = arg_val(&args, i)?.parse().context("--trait-refresh-cycles must be a number")?; explicit.insert("trait_refresh_cycles"); }
            "--result-cache-cycles" => { i += 1; cli.result_cache_cycles = arg_val(&args, i)?.parse().context("--result-cache-cycles must be a number")?; explicit.insert("result_cache_cycles"); }
            "--dry-run" => { cli.dry_run = true; }
            "--once" => { cli.once = true; }
            other if other.starts_with('-') => bail!("unknown flag: {}", other),
            _ => {
                positional.push(args[i].clone());
                in_subcommand = true;
            }
        }
        i += 1;
    }

    // Load config file from data dir — CLI flags take precedence.
    let cfg = config::load(&cli.data_dir);
    if !explicit.contains("server") {
        if let Some(v) = cfg.get("server") { cli.server = v.clone(); }
    }
    if !explicit.contains("api_addr") {
        if let Some(v) = cfg.get("api_addr") { cli.api_addr = v.clone(); }
    }
    if !explicit.contains("log_level") {
        if let Some(v) = cfg.get("log_level") { cli.log_level = v.clone(); }
    }
    if !explicit.contains("executors_dir") {
        if let Some(v) = cfg.get("executors_dir") { cli.executors_dir = v.clone(); }
    }
    if !explicit.contains("snapshot_dir") {
        if let Some(v) = cfg.get("snapshot_dir") { cli.snapshot_dir = v.clone(); }
    }
    if !explicit.contains("snapshot_retention") {
        if let Some(v) = cfg.get("snapshot_retention") {
            if let Ok(n) = v.parse() { cli.snapshot_retention = n; }
        }
    }
    if !explicit.contains("trait_refresh_cycles") {
        if let Some(v) = cfg.get("trait_refresh_cycles") {
            if let Ok(n) = v.parse() { cli.trait_refresh_cycles = n; }
        }
    }
    if !explicit.contains("result_cache_cycles") {
        if let Some(v) = cfg.get("result_cache_cycles") {
            if let Ok(n) = v.parse() { cli.result_cache_cycles = n; }
        }
    }

    if let Some(cmd) = positional.first() {
        cli.command = Some(match cmd.as_str() {
            "version" => Commands::Version,
            "status" => Commands::Status,
            "run" => {
                let mut module = None;
                let mut dry_run = false;
                let mut no_daemon = false;
                let mut j = 1;
                while j < positional.len() {
                    match positional[j].as_str() {
                        "--module" => { j += 1; module = Some(positional.get(j).cloned().context("--module requires a value")?); }
                        "--dry-run" => { dry_run = true; }
                        "--no-daemon" => { no_daemon = true; }
                        _ => bail!("unknown run flag: {}", positional[j]),
                    }
                    j += 1;
                }
                Commands::Run { module, dry_run, no_daemon }
            }
            "register" => {
                let mut token = None;
                let mut force = false;
                let mut j = 1;
                while j < positional.len() {
                    match positional[j].as_str() {
                        "--token" => { j += 1; token = Some(positional.get(j).cloned().context("--token requires a value")?); }
                        "--force" => { force = true; }
                        _ => bail!("unknown register flag: {}", positional[j]),
                    }
                    j += 1;
                }
                Commands::Register { token: token.context("register requires --token")?, force }
            }
            "bootstrap" => {
                let mut server = None;
                let mut token = None;
                let mut install_service = false;
                let mut grpc_port = "1530".to_string();
                let mut force = false;
                let mut j = 1;
                while j < positional.len() {
                    match positional[j].as_str() {
                        "--server" => { j += 1; server = Some(positional.get(j).cloned().context("--server requires a value")?); }
                        "--token" => { j += 1; token = Some(positional.get(j).cloned().context("--token requires a value")?); }
                        "--install-service" => { install_service = true; }
                        "--grpc-port" => { j += 1; grpc_port = positional.get(j).cloned().context("--grpc-port requires a value")?; }
                        "--force" => { force = true; }
                        _ => bail!("unknown bootstrap flag: {}", positional[j]),
                    }
                    j += 1;
                }
                Commands::Bootstrap {
                    server: server.context("bootstrap requires --server")?,
                    token,
                    install_service,
                    grpc_port,
                    force,
                }
            }
            "traits" => {
                Commands::Traits { collectors: positional[1..].to_vec() }
            }
            other => bail!("unknown command: {}\nRun with --help for usage", other),
        });
    }

    Ok(cli)
}

fn arg_val(args: &[String], i: usize) -> Result<String> {
    args.get(i).cloned().context("expected value after flag")
}

fn print_usage() {
    let data_dir = platform::paths::default_data_dir();
    let exec_dir = platform::paths::default_executors_dir();
    let snap_dir = platform::paths::default_snapshot_dir();
    eprintln!("hobnob — Hobnob configuration management agent

USAGE:
    hobnob [FLAGS] [COMMAND]

FLAGS:
    --data-dir <PATH>             Agent data directory [default: {}]
    --server <ADDR>               Server gRPC address [default: localhost:1530]
    --ca-file <PATH>              CA certificate file
    --cert-file <PATH>            Client certificate file
    --key-file <PATH>             Client key file
    --log-level <LEVEL>           Log level [default: info]
    --api-addr <URL>              Server REST API address [default: https://localhost:8443]
    --executors-dir <PATH>        Custom executor scripts [default: {}]
    --snapshot-dir <PATH>         File snapshots directory [default: {}]
    --snapshot-retention <N>      Max snapshots per file [default: 5]
    --trait-refresh-cycles <N>    Trait refresh interval in cycles [default: 10]
    --result-cache-cycles <N>     Result cache lifetime in cycles [default: 5]
    --dry-run                     Preview mode
    --once                        Run a single convergence cycle and exit

COMMANDS:
    run         Run a single convergence cycle and exit (starts daemon if stopped)
                  --no-daemon    Don't start the daemon after the run
    register    Register with the server (--token <TOKEN> [--force])
    bootstrap   Bootstrap agent (--server <URL> [--token <TOKEN>] [--install-service] [--force])
    version     Print agent version
    status      Show agent state (config, cached policy, pending results)
    traits      Print collected traits as JSON [COLLECTORS...]

With no command, runs as a daemon.

Settings are resolved in order: CLI flags > <data-dir>/config > defaults.
The config file is written automatically during register/bootstrap.", data_dir.display(), exec_dir.display(), snap_dir.display());
}

fn setup_logger(level: &str) {
    let level = match level {
        "trace" => tracing::Level::TRACE,
        "debug" => tracing::Level::DEBUG,
        "warn" => tracing::Level::WARN,
        "error" => tracing::Level::ERROR,
        _ => tracing::Level::INFO,
    };
    tracing_subscriber::fmt().with_max_level(level).init();
}

/// Ensure the agent daemon service is running. Called after `hobnob run`
/// so the daemon resumes if it was stopped for manual testing.
fn ensure_daemon_running() {
    use std::process::Command;

    #[cfg(unix)]
    {
        if std::path::Path::new("/run/systemd/system").exists() {
            // systemd
            let running = Command::new("systemctl")
                .args(["is-active", "--quiet", "hobnob-thrall"])
                .status()
                .map(|s| s.success())
                .unwrap_or(false);
            if !running {
                info!("starting hobnob-thrall daemon");
                match Command::new("systemctl").args(["start", "hobnob-thrall"]).status() {
                    Ok(s) if s.success() => info!("hobnob-thrall daemon started"),
                    Ok(s) => warn!("failed to start hobnob-thrall (exit {})", s),
                    Err(e) => warn!("failed to start hobnob-thrall: {}", e),
                }
            }
        } else if std::path::Path::new("/Library/LaunchDaemons/com.hobnob.thrall.plist").exists() {
            // launchd (macOS)
            let running = Command::new("launchctl")
                .args(["list", "com.hobnob.thrall"])
                .status()
                .map(|s| s.success())
                .unwrap_or(false);
            if !running {
                info!("starting hobnob-thrall daemon");
                let _ = Command::new("launchctl")
                    .args(["load", "-w", "/Library/LaunchDaemons/com.hobnob.thrall.plist"])
                    .status();
            }
        } else if std::path::Path::new("/etc/rc.d/hobnob").exists()
            || std::path::Path::new("/usr/local/etc/rc.d/hobnob").exists()
        {
            // BSD rc.d
            let running = Command::new("service")
                .args(["hobnob", "status"])
                .status()
                .map(|s| s.success())
                .unwrap_or(false);
            if !running {
                info!("starting hobnob daemon");
                let _ = Command::new("service").args(["hobnob", "start"]).status();
            }
        }
        // No known service manager — skip silently.
    }

    #[cfg(windows)]
    {
        let running = Command::new("sc.exe")
            .args(["query", "hobnob-thrall"])
            .output()
            .map(|o| String::from_utf8_lossy(&o.stdout).contains("RUNNING"))
            .unwrap_or(false);
        if !running {
            info!("starting hobnob-thrall service");
            let _ = Command::new("sc.exe").args(["start", "hobnob-thrall"]).status();
        }
    }
}

/// Read thrall UUID from <data-dir>/thrall.uuid.
fn load_thrall_id(data_dir: &std::path::Path) -> Result<String> {
    let path = data_dir.join("thrall.uuid");
    let data = std::fs::read_to_string(&path)
        .with_context(|| format!("read thrall UUID from {} (have you run 'hobnob register'?)", path.display()))?;
    Ok(data.trim().to_string())
}

/// Write thrall UUID to <data-dir>/thrall.uuid.
fn save_thrall_id(data_dir: &std::path::Path, id: &str) -> Result<()> {
    let path = data_dir.join("thrall.uuid");
    std::fs::write(&path, format!("{}\n", id))
        .with_context(|| format!("write thrall UUID to {}", path.display()))?;
    // Set permissions to 0600
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))?;
    }
    Ok(())
}

/// Save spoke address to <data-dir>/spoke.addr.
fn save_spoke_addr(data_dir: &std::path::Path, addr: &str) -> Result<()> {
    let path = data_dir.join("spoke.addr");
    if addr.is_empty() {
        let _ = std::fs::remove_file(&path);
        return Ok(());
    }
    std::fs::write(&path, format!("{}\n", addr))?;
    Ok(())
}

/// Load spoke address. Returns empty string if not set.
fn load_spoke_addr(data_dir: &std::path::Path) -> String {
    let path = data_dir.join("spoke.addr");
    std::fs::read_to_string(path)
        .map(|s| s.trim().to_string())
        .unwrap_or_default()
}

/// Save server's bundle-signing public key to <data-dir>/server_pubkey.pem.
fn save_server_pubkey(data_dir: &std::path::Path, pem: &str) -> Result<()> {
    let path = data_dir.join("server_pubkey.pem");
    std::fs::write(&path, pem)?;
    Ok(())
}

/// Load server's bundle-signing public key. Returns empty string if not set.
fn load_server_pubkey(data_dir: &std::path::Path) -> String {
    let path = data_dir.join("server_pubkey.pem");
    std::fs::read_to_string(path)
        .map(|s| s.trim().to_string())
        .unwrap_or_default()
}

/// Resolve TLS paths: explicit flags → bootstrap certs in data dir.
fn resolve_tls_paths(
    cli: &Cli,
    data_dir: &std::path::Path,
) -> (Option<String>, Option<String>, Option<String>) {
    let ca = cli.ca_file.clone();
    let cert = cli.cert_file.clone();
    let key = cli.key_file.clone();

    if ca.is_none() && auth::keypair::has_bootstrap_certs(data_dir) {
        let certs_dir = data_dir.join("certs");
        return (
            Some(certs_dir.join("ca.crt").to_string_lossy().into()),
            Some(certs_dir.join("tls.crt").to_string_lossy().into()),
            Some(certs_dir.join("tls.key").to_string_lossy().into()),
        );
    }

    (ca, cert, key)
}

/// Run a single convergence cycle with color-coded trace output.
async fn run_once(
    cli: &Cli,
    module_filter: Option<String>,
    dry_run: bool,
) -> Result<()> {
    let (_, priv_key) = auth::keypair::load_or_generate(&cli.data_dir)?;
    let thrall_id = load_thrall_id(&cli.data_dir)?;

    let (ca, cert, key) = resolve_tls_paths(cli, &cli.data_dir);

    // Use spoke address if hub routed us to a spoke.
    let addr = {
        let spoke = load_spoke_addr(&cli.data_dir);
        if spoke.is_empty() {
            cli.server.clone()
        } else {
            info!(spoke_addr = %spoke, "using spoke address from enrollment routing");
            spoke
        }
    };

    let mut client = checkin::client::Client::connect(
        &addr,
        ca.as_deref(),
        cert.as_deref(),
        key.as_deref(),
        &thrall_id,
        priv_key,
    )
    .await?;

    info!(server = %addr, thrall_id = %thrall_id, version = VERSION, "starting check-in");

    // Check-in with server.
    let resp = client.check_in("", None, 0, None).await?;

    for w in &resp.warnings {
        warn!("server warning: {}", w);
    }

    if resp.no_change {
        info!("server reports no change");
        return Ok(());
    }

    let force_update = resp.force_update;

    // Collect system traits for policy evaluation and executor detection.
    let collected_traits = traits::collect_all(&traits::default_collectors()).await;

    // Get modules from either policy bundle (agent-side eval) or compiled modules.
    let (modules, bundle_for_trace) = if let Some(ref bundle) = resp.policy_bundle {
        info!(
            policy_version = bundle.policy_version,
            modules = bundle.modules.len(),
            "received policy bundle, evaluating locally"
        );

        match policy::eval::eval_bundle(Some(bundle), &collected_traits) {
            Ok(modules) => (modules, Some(bundle.clone())),
            Err(e) => {
                warn!("policy evaluation failed, running with compiled modules: {}", e);
                (resp.modules.clone(), resp.policy_bundle.clone())
            }
        }
    } else {
        info!(modules = resp.modules.len(), "received compiled modules");
        (resp.modules, resp.policy_bundle)
    };

    if modules.is_empty() {
        warn!("no modules to apply");
        return Ok(());
    }

    // Build executor map with snapshot manager.
    let os_family = collected_traits.pointer("/os/family")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let os_distro = collected_traits.pointer("/os/distro")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let snapshot_mgr = snapshots::Manager::new(&cli.snapshot_dir, cli.snapshot_retention);
    let mut executors = runner::build_executors_with_snapshots(&os_family, &os_distro, Some(snapshot_mgr));

    // Load custom executors.
    runner::load_custom_executors(&mut executors, &cli.executors_dir);

    // Acquire convergence lock — prevents daemon + manual run collisions.
    let _lock = runner::lock::ConvergenceLock::try_acquire(&cli.data_dir)?
        .ok_or_else(|| anyhow::anyhow!("another convergence is running (lock held)"))?;

    // Run convergence.
    let run_id = uuid::Uuid::new_v4().to_string();
    let mut cache = runner::cache::ResultCache::new(cli.result_cache_cycles);

    let opts = runner::RunOptions {
        dry_run,
        module_filter,
        run_id: run_id.clone(),
    };

    let results = runner::converge(&modules, &executors, &mut cache, &opts, &collected_traits);

    // Print color-coded trace output to stderr.
    print_trace(&bundle_for_trace, &results, dry_run);

    // Report results back to server.
    if !dry_run {
        let proto_results: Vec<proto::ModuleResult> = results
            .iter()
            .map(|r| proto::ModuleResult {
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

        if let Err(e) = client.report_result(&run_id, proto_results).await {
            warn!(error = %e, "failed to report results to server");
        }
    }

    // Self-update: if server flagged force_update, download and replace binary.
    if force_update {
        info!("server requested agent update");
        let tls_certs = if auth::keypair::has_bootstrap_certs(&cli.data_dir) {
            Some(update::TlsCerts {
                ca_cert: auth::keypair::ca_cert_path(&cli.data_dir).to_string_lossy().to_string(),
                client_cert: auth::keypair::tls_cert_path(&cli.data_dir).to_string_lossy().to_string(),
                client_key: auth::keypair::tls_key_path(&cli.data_dir).to_string_lossy().to_string(),
            })
        } else {
            None
        };
        update::force_self_update(&cli.api_addr, tls_certs.as_ref()).await;
    }

    let error_count = results.iter().filter(|r| r.error.is_some()).count();
    if error_count > 0 {
        anyhow::bail!("{} resource(s) failed during convergence", error_count);
    }

    Ok(())
}

/// Print a color-coded trace of the convergence run to stderr.
fn print_trace(
    bundle: &Option<proto::PolicyBundle>,
    results: &[executors::ResourceResult],
    dry_run: bool,
) {
    use std::io::Write;
    let stderr = std::io::stderr();
    let mut out = stderr.lock();

    // ANSI color codes.
    const BOLD: &str = "\x1b[1m";
    const DIM: &str = "\x1b[2m";
    const RED: &str = "\x1b[31m";
    const GREEN: &str = "\x1b[32m";
    const YELLOW: &str = "\x1b[33m";
    const CYAN: &str = "\x1b[36m";
    const RESET: &str = "\x1b[0m";

    // Print trace header from policy bundle.
    if let Some(b) = bundle {
        if dry_run {
            let _ = writeln!(out, "\n{BOLD}{CYAN}--- Dry Run ---{RESET}");
        } else {
            let _ = writeln!(out, "\n{BOLD}{GREEN}--- Run ---{RESET}");
        }
        if !b.matched_pattern.is_empty() {
            let _ = writeln!(out, "{BOLD}Match:{RESET}       {}", b.matched_pattern);
        }
        if !b.environment.is_empty() {
            let _ = writeln!(out, "{BOLD}Environment:{RESET} {}", b.environment);
        }
        if !b.roles.is_empty() {
            let _ = writeln!(out, "{BOLD}Roles:{RESET}       {}", b.roles.join(", "));
        }
        let mod_names: Vec<&str> = b.modules.iter().map(|m| m.name.as_str()).collect();
        if !mod_names.is_empty() {
            let _ = writeln!(out, "{BOLD}Modules:{RESET}     {}", mod_names.join(", "));
        }
        let _ = writeln!(out);
    }

    // Print results grouped by module.
    let mut cur_module = "";
    for r in results {
        if r.module_name != cur_module {
            cur_module = &r.module_name;
            let _ = writeln!(out, "  {BOLD}Module: {cur_module}{RESET}");
        }

        let (status, color) = if let Some(ref err) = r.error {
            (format!("ERROR: {}", err), format!("{BOLD}{RED}"))
        } else {
            let s = match (&r.action, &r.skip_reason) {
                (executors::Action::Skipped, Some(reason)) => format!("skipped ({})", reason),
                _ => r.action.to_string(),
            };
            let c = match r.action {
                executors::Action::Created => GREEN.to_string(),
                executors::Action::Updated => YELLOW.to_string(),
                executors::Action::Deleted => RED.to_string(),
                executors::Action::Skipped => CYAN.to_string(),
                executors::Action::None => DIM.to_string(),
            };
            (s, c)
        };

        let _ = writeln!(
            out,
            "    {DIM}[{typ}]{RESET} {name:<24} {color}{status}{RESET}",
            typ = r.resource_type,
            name = r.resource_name,
        );
    }
}

/// Run the daemon loop with polling + streaming modes.
async fn run_daemon(cli: &Cli) -> Result<()> {
    let (_, priv_key) = auth::keypair::load_or_generate(&cli.data_dir)?;
    let thrall_id = load_thrall_id(&cli.data_dir)?;
    let (ca, cert, key) = resolve_tls_paths(cli, &cli.data_dir);

    // Use spoke address if hub routed us to a spoke.
    let addr = {
        let spoke = load_spoke_addr(&cli.data_dir);
        if spoke.is_empty() {
            cli.server.clone()
        } else {
            info!(spoke_addr = %spoke, "using spoke address from enrollment routing");
            spoke
        }
    };

    let mut client = checkin::client::Client::connect(
        &addr,
        ca.as_deref(),
        cert.as_deref(),
        key.as_deref(),
        &thrall_id,
        priv_key,
    )
    .await?;

    client.set_agent_version(VERSION);

    // Load fallback servers from config for failover.
    let cfg = config::load(&cli.data_dir);
    let fallbacks = config::parse_fallback_servers(&cfg);
    if !fallbacks.is_empty() {
        info!(fallback_servers = ?fallbacks, "configured failover servers");
        client.set_fallback_servers(fallbacks);
    }

    info!(
        server = %addr,
        thrall_id = %thrall_id,
        version = VERSION,
        "starting daemon loop"
    );

    // Build executors with snapshot manager.
    let collected_traits = traits::collect_all(&traits::default_collectors()).await;
    let os_family = collected_traits
        .pointer("/os/family")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let os_distro = collected_traits
        .pointer("/os/distro")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let snapshot_mgr = snapshots::Manager::new(&cli.snapshot_dir, cli.snapshot_retention);
    let mut executors = runner::build_executors_with_snapshots(&os_family, &os_distro, Some(snapshot_mgr));
    runner::load_custom_executors(&mut executors, &cli.executors_dir);

    // Set up cancellation on SIGINT/SIGTERM.
    let cancel = tokio_util::sync::CancellationToken::new();
    let cancel_clone = cancel.clone();
    tokio::spawn(async move {
        let _ = tokio::signal::ctrl_c().await;
        info!("received shutdown signal");
        cancel_clone.cancel();
    });

    // Open persistent state store for offline convergence.
    let state_dir = platform::paths::default_state_dir();
    let state_store = match state::StateStore::open(&state_dir) {
        Ok(s) => {
            info!(path = %state_dir.display(), "persistent state store ready");
            Some(s)
        }
        Err(e) => {
            warn!(error = %e, "could not open state store, offline convergence disabled");
            None
        }
    };

    // Configure daemon loop.
    let traits_cache_dir = cli.data_dir.join("traits").to_string_lossy().to_string();
    let cfg = checkin::loop_impl::LoopConfig {
        trait_refresh_cycles: cli.trait_refresh_cycles,
        result_cache_cycles: cli.result_cache_cycles,
        traits_cache_dir: Some(traits_cache_dir),
        update_cfg: Some(checkin::loop_impl::UpdateConfig {
            api_addr: cli.api_addr.clone(),
            current_version: VERSION.to_string(),
            tls_certs: if auth::keypair::has_bootstrap_certs(&cli.data_dir) {
                Some(update::TlsCerts {
                    ca_cert: auth::keypair::ca_cert_path(&cli.data_dir).to_string_lossy().to_string(),
                    client_cert: auth::keypair::tls_cert_path(&cli.data_dir).to_string_lossy().to_string(),
                    client_key: auth::keypair::tls_key_path(&cli.data_dir).to_string_lossy().to_string(),
                })
            } else {
                None
            },
        }),
        state_store,
        server_pubkey_pem: load_server_pubkey(&cli.data_dir),
        data_dir: Some(cli.data_dir.clone()),
    };

    checkin::loop_impl::daemon_loop(cancel, &mut client, &executors, None, cfg).await
}

/// Get the preferred outbound IP.
fn get_outbound_ip() -> String {
    use std::net::UdpSocket;
    UdpSocket::bind("0.0.0.0:0")
        .and_then(|s| {
            s.connect("8.8.8.8:80")?;
            s.local_addr()
        })
        .map(|addr| addr.ip().to_string())
        .unwrap_or_else(|_| "127.0.0.1".to_string())
}

/// Platform-specific service installation.
#[cfg(target_os = "linux")]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
    data_dir: &std::path::Path,
) -> Result<String> {
    bootstrap::systemd::install_service(bootstrap_server, grpc_port, data_dir).await
}

#[cfg(target_os = "macos")]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
    data_dir: &std::path::Path,
) -> Result<String> {
    bootstrap::launchd::install_service(bootstrap_server, grpc_port, data_dir).await
}

#[cfg(any(target_os = "freebsd", target_os = "netbsd"))]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
    data_dir: &std::path::Path,
) -> Result<String> {
    bootstrap::rc_d::install_service(bootstrap_server, grpc_port, data_dir).await
}

#[cfg(target_os = "openbsd")]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
    data_dir: &std::path::Path,
) -> Result<String> {
    bootstrap::rcctl::install_service(bootstrap_server, grpc_port, data_dir).await
}

#[cfg(target_os = "illumos")]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
    data_dir: &std::path::Path,
) -> Result<String> {
    bootstrap::smf::install_service(bootstrap_server, grpc_port, data_dir).await
}

#[cfg(windows)]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
    data_dir: &std::path::Path,
) -> Result<String> {
    bootstrap::windows_service::install_service(bootstrap_server, grpc_port, data_dir).await
}

#[cfg(not(any(target_os = "linux", target_os = "macos", target_os = "freebsd", target_os = "netbsd", target_os = "openbsd", target_os = "illumos", windows)))]
async fn install_platform_service(
    _bootstrap_server: &str,
    _grpc_port: &str,
    _data_dir: &std::path::Path,
) -> Result<String> {
    bail!("service installation is not supported on this platform")
}

/// Show local agent state: config, cached policy, pending results.
/// Reads only from local files and the LMDB state store — no server communication.
fn run_status(cli: &Cli) -> Result<()> {
    use std::time::{SystemTime, UNIX_EPOCH};

    // Load config.
    let cfg = config::load(&cli.data_dir);

    // Thrall ID.
    let thrall_id = match load_thrall_id(&cli.data_dir) {
        Ok(id) => id,
        Err(_) => "(not registered)".to_string(),
    };

    // Server address.
    let server = cfg.get("server").cloned().unwrap_or_else(|| cli.server.clone());

    // Fallback servers.
    let fallbacks = config::parse_fallback_servers(&cfg);
    let fallback_str = if fallbacks.is_empty() {
        "none".to_string()
    } else {
        fallbacks.join(", ")
    };

    // Spoke address.
    let spoke = load_spoke_addr(&cli.data_dir);

    let data_dir = cli.data_dir.display();
    let state_dir = platform::paths::default_state_dir();

    println!("Thrall ID:        {}", thrall_id);
    println!("Server:           {}", server);
    if !spoke.is_empty() {
        println!("Spoke:            {}", spoke);
    }
    println!("Fallback servers: {}", fallback_str);
    println!("Data directory:   {}", data_dir);
    println!("State store:      {}", state_dir.display());

    // Server pubkey from file.
    let server_pubkey = load_server_pubkey(&cli.data_dir);
    let pubkey_status = if server_pubkey.is_empty() { "absent" } else { "present" };

    // Open state store (read-only). Root-owned LMDB files enforce access control.
    let store = match state::StateStore::open(&state_dir) {
        Ok(s) => s,
        Err(e) => {
            println!();
            println!("Policy:           (state store unavailable: {})", e);
            println!("Traits:           (state store unavailable)");
            println!("Pending results:  (state store unavailable)");
            println!("Server pubkey:    {}", pubkey_status);
            return Ok(());
        }
    };

    let rtxn = store.read_txn()?;

    // Policy bundle.
    println!();
    match store.policy.load(&rtxn) {
        Ok(Some((version, bundle))) => {
            let mod_count = bundle.modules.len();
            let mod_names: Vec<&str> = bundle.modules.iter().map(|m| m.name.as_str()).collect();
            let resource_count: usize = bundle.modules.iter().map(|m| m.resources.len()).sum();

            println!("Policy:");
            println!("  Cached:         yes");
            println!("  Version:        {}", version);
            if !bundle.matched_pattern.is_empty() {
                println!("  Match:          {}", bundle.matched_pattern);
            }
            if !bundle.environment.is_empty() {
                println!("  Environment:    {}", bundle.environment);
            }
            if !bundle.roles.is_empty() {
                println!("  Roles:          {}", bundle.roles.join(", "));
            }
            println!("  Modules:        {} ({})", mod_count, mod_names.join(", "));
            println!("  Resources:      {}", resource_count);

            // Bundle max age.
            if bundle.max_age_seconds > 0 {
                println!("  Max age:        {}s", bundle.max_age_seconds);
            } else {
                println!("  Max age:        unlimited");
            }

            // List resources by module (type: name only, no secrets).
            for m in &bundle.modules {
                if m.resources.is_empty() {
                    continue;
                }
                println!("  [{}]", m.name);
                for r in &m.resources {
                    println!("    {}: {}", r.r#type, r.name);
                }
            }
        }
        Ok(None) => {
            println!("Policy:");
            println!("  Cached:         no");
        }
        Err(e) => {
            println!("Policy:           (error reading: {})", e);
        }
    }

    // Traits cache.
    println!();
    match store.traits.load_any(&rtxn) {
        Ok(Some(traits_bytes)) => {
            let collector_count = match serde_json::from_slice::<serde_json::Value>(&traits_bytes) {
                Ok(serde_json::Value::Object(map)) => map.len(),
                _ => 0,
            };
            let ts = store.traits.load_timestamp(&rtxn).unwrap_or(None);
            let age_str = match ts {
                Some(ts_secs) => {
                    let now = SystemTime::now()
                        .duration_since(UNIX_EPOCH)
                        .unwrap_or_default()
                        .as_secs();
                    format_duration(now.saturating_sub(ts_secs))
                }
                None => "unknown".to_string(),
            };
            println!("Traits:");
            println!("  Cached:         yes");
            println!("  Collectors:     {}", collector_count);
            println!("  Cache age:      {}", age_str);
        }
        Ok(None) => {
            println!("Traits:");
            println!("  Cached:         no");
        }
        Err(e) => {
            println!("Traits:           (error reading: {})", e);
        }
    }

    // Pending results.
    println!();
    match store.pending.len(&rtxn) {
        Ok(count) => println!("Pending results:  {}", count),
        Err(e) => println!("Pending results:  (error reading: {})", e),
    }

    // Server pubkey.
    println!("Server pubkey:    {}", pubkey_status);

    Ok(())
}

/// Format seconds into a human-readable duration string.
fn format_duration(secs: u64) -> String {
    if secs < 60 {
        format!("{}s", secs)
    } else if secs < 3600 {
        format!("{}m {}s", secs / 60, secs % 60)
    } else if secs < 86400 {
        let h = secs / 3600;
        let m = (secs % 3600) / 60;
        format!("{}h {}m", h, m)
    } else {
        let d = secs / 86400;
        let h = (secs % 86400) / 3600;
        format!("{}d {}h", d, h)
    }
}

#[tokio::main]
async fn main() -> Result<()> {
    // Install the default TLS crypto provider before any TLS operations.
    rustls::crypto::ring::default_provider()
        .install_default()
        .expect("failed to install default CryptoProvider");

    let cli = parse_args()?;
    setup_logger(&cli.log_level);

    // Ensure data directory exists.
    std::fs::create_dir_all(&cli.data_dir)
        .with_context(|| format!("create data directory {}", cli.data_dir.display()))?;

    match &cli.command {
        Some(Commands::Version) => {
            println!("{}", VERSION);
            Ok(())
        }

        Some(Commands::Status) => {
            run_status(&cli)
        }

        Some(Commands::Register { token, force }) => {
            let (pub_key, priv_key) = auth::keypair::load_or_generate(&cli.data_dir)?;
            let pub_pem = auth::keypair::public_key_pem(&pub_key)?;

            let hostname = hostname::get()
                .context("get hostname")?
                .to_string_lossy()
                .to_string();
            let ip_addr = get_outbound_ip();

            info!(server = %cli.server, hostname = %hostname, ip = %ip_addr, "registering with server");

            let (ca, cert, key) = resolve_tls_paths(&cli, &cli.data_dir);
            let mut client = checkin::client::Client::connect(
                &cli.server,
                ca.as_deref(),
                cert.as_deref(),
                key.as_deref(),
                "",
                priv_key,
            )
            .await?;

            let resp = client.register(&hostname, &ip_addr, &pub_pem, token, *force).await?;

            if !resp.success {
                anyhow::bail!(
                    "registration rejected: {}\n\nhint: check that the token is valid and unused, and that the hostname {:?} matches an approved pattern",
                    resp.message, hostname
                );
            }

            save_thrall_id(&cli.data_dir, &resp.thrall_id)?;

            // Persist server address so subsequent runs don't need --server.
            if let Err(e) = config::save(&cli.data_dir, &[("server", &cli.server)]) {
                warn!(error = %e, "failed to save config");
            }

            // Store server's bundle-signing public key for future verification.
            if !resp.server_pubkey_pem.is_empty() {
                save_server_pubkey(&cli.data_dir, &resp.server_pubkey_pem)?;
                info!("stored server bundle-signing public key");
            }

            if !resp.spoke_addr.is_empty() {
                save_spoke_addr(&cli.data_dir, &resp.spoke_addr)?;
                info!(spoke_addr = %resp.spoke_addr, "hub routed to spoke");
            }

            // Persist fallback servers for agent failover.
            if !resp.fallback_servers.is_empty() {
                let fb = resp.fallback_servers.join(",");
                if let Err(e) = config::save(&cli.data_dir, &[("fallback_servers", &fb)]) {
                    warn!(error = %e, "failed to save fallback_servers to config");
                }
                info!(fallback_servers = %fb, "stored fallback servers for failover");
            }

            info!(thrall_id = %resp.thrall_id, message = %resp.message, "registration successful");
            Ok(())
        }

        Some(Commands::Traits { collectors: args }) => {
            // Suppress tracing for clean JSON output.
            // (Logger already initialized, but traits output goes to stdout only.)

            let all_collectors = traits::default_collectors();

            if args.is_empty() {
                // No args: collect everything.
                let collected = traits::collect_all(&all_collectors).await;
                println!("{}", serde_json::to_string_pretty(&collected)?);
                return Ok(());
            }

            // If first arg contains a dot, treat as dot-path query.
            if args[0].contains('.') {
                let collected = traits::collect_all(&all_collectors).await;
                match traits::traverse_dot_path(&collected, &args[0]) {
                    Some(val) => {
                        println!("{}", traits::format_value(&val));
                    }
                    None => {
                        eprintln!("trait path '{}' not found", args[0]);
                        std::process::exit(1);
                    }
                }
                return Ok(());
            }

            // Otherwise, treat args as collector name filters.
            let mut filtered = Vec::new();
            let mut unknown = Vec::new();
            for name in args {
                match traits::CollectorKind::from_name(name) {
                    Some(kind) => filtered.push(kind),
                    None => unknown.push(name.as_str()),
                }
            }

            if !unknown.is_empty() {
                let available: Vec<&str> = all_collectors.iter().map(|c| c.name()).collect();
                eprintln!(
                    "unknown collector(s): {} (available: {})",
                    unknown.join(", "),
                    available.join(", "),
                );
                std::process::exit(1);
            }

            let collected = traits::collect_all(&filtered).await;
            println!("{}", serde_json::to_string_pretty(&collected)?);
            Ok(())
        }

        Some(Commands::Bootstrap {
            server: bootstrap_server,
            token,
            install_service,
            grpc_port,
            force,
        }) => {
            // Load or generate ED25519 keypair (for request signing).
            let (pub_key, _) = auth::keypair::load_or_generate(&cli.data_dir)?;
            let pub_pem = auth::keypair::public_key_pem(&pub_key)?;

            // Generate ECDSA P-256 TLS keypair and CSR.
            let tls_key = auth::keypair::generate_tls_key()?;
            let host = hostname::get()
                .context("get hostname")?
                .to_string_lossy()
                .to_string();
            let csr_pem = auth::keypair::generate_csr(&tls_key, &host)?;
            let ip_addr = get_outbound_ip();

            info!(
                server = %bootstrap_server,
                hostname = %host,
                ip = %ip_addr,
                "bootstrapping with server"
            );

            // Call bootstrap register endpoint.
            let req = bootstrap::client::RegisterRequest {
                hostname: host,
                ip_address: ip_addr,
                pubkey_pem: pub_pem,
                csr_pem,
                token: token.clone(),
                force: *force,
            };
            let resp = bootstrap::client::register(bootstrap_server, &req).await?;

            // Save TLS artifacts and thrall UUID.
            auth::keypair::save_tls_key(&cli.data_dir, &tls_key)?;
            auth::keypair::save_tls_cert(&cli.data_dir, &resp.signed_cert_pem)?;
            auth::keypair::save_ca_cert(&cli.data_dir, &resp.ca_cert_pem)?;
            save_thrall_id(&cli.data_dir, &resp.thrall_id)?;

            // Persist server address so subsequent runs don't need --server.
            let grpc_addr = format!("{}:{}", bootstrap_server.trim_start_matches("http://").trim_start_matches("https://").split(':').next().unwrap_or(bootstrap_server), grpc_port);
            let mut config_pairs: Vec<(&str, &str)> = vec![("server", &grpc_addr), ("api_addr", bootstrap_server)];
            let fb_str = resp.fallback_servers.join(",");
            if !resp.fallback_servers.is_empty() {
                config_pairs.push(("fallback_servers", &fb_str));
            }
            if let Err(e) = config::save(&cli.data_dir, &config_pairs) {
                warn!(error = %e, "failed to save config");
            }
            if !resp.fallback_servers.is_empty() {
                info!(fallback_servers = %fb_str, "stored fallback servers for failover");
            }

            info!(
                thrall_id = %resp.thrall_id,
                tls_cert = %auth::keypair::tls_cert_path(&cli.data_dir).display(),
                ca_cert = %auth::keypair::ca_cert_path(&cli.data_dir).display(),
                "bootstrap successful"
            );

            // Install system service if requested.
            if *install_service {
                let install_result = install_platform_service(
                    bootstrap_server,
                    grpc_port,
                    &cli.data_dir,
                )
                .await;

                match install_result {
                    Ok(grpc_addr) => {
                        // Verify connectivity with a single check-in.
                        info!("verifying agent check-in...");
                        let (_, priv_key) = auth::keypair::load_or_generate(&cli.data_dir)?;
                        let (ca, cert, key) = resolve_tls_paths(&cli, &cli.data_dir);
                        match checkin::client::Client::connect(
                            &grpc_addr,
                            ca.as_deref(),
                            cert.as_deref(),
                            key.as_deref(),
                            &resp.thrall_id,
                            priv_key,
                        )
                        .await
                        {
                            Ok(mut client) => {
                                match client.check_in("", None, 0, None).await {
                                    Ok(_) => info!("check-in verified, service is running"),
                                    Err(e) => warn!(error = %e, "verification check-in failed — service is installed but may not reach the server"),
                                }
                            }
                            Err(e) => warn!(error = %e, "could not connect for verification — service is installed but may not reach the server"),
                        }
                    }
                    Err(e) => {
                        warn!(error = %e, "service installation failed");
                        eprintln!(
                            "Service install failed: {}\nStart the agent manually:\n  hobnob --server <grpc-host>:1530 --data-dir {}",
                            e,
                            cli.data_dir.display()
                        );
                    }
                }
            }

            Ok(())
        }

        Some(Commands::Run { module, dry_run: run_dry_run, no_daemon }) => {
            let result = run_once(
                &cli,
                module.clone(),
                cli.dry_run || *run_dry_run,
            )
            .await;

            if !no_daemon {
                ensure_daemon_running();
            }

            result
        }

        None if cli.once => {
            // --once flag: single convergence cycle and exit.
            run_once(&cli, None, cli.dry_run).await
        }

        None => {
            // Daemon mode — persistent loop with backoff + stream mode.
            run_daemon(&cli).await
        }
    }
}
