// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

pub mod auth;
pub mod bootstrap;
pub mod checkin;
pub mod config;
pub mod customtraits;
pub mod executors;
pub mod guardrails;
pub mod platform;
pub mod policy;
pub mod runner;
pub mod snapshots;
pub mod state;
pub mod task;
pub mod traits;
pub mod transport;
pub mod update;

pub mod proto {
    tonic::include_proto!("hobnob.v1");
}

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
    command: Option<Commands>,
}

enum Commands {
    Run {
        module: Option<String>,
        dry_run: bool,
    },
    Register {
        token: String,
    },
    Bootstrap {
        server: String,
        token: Option<String>,
        install_service: bool,
        grpc_port: String,
    },
    Version,
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
        api_addr: "http://localhost:8181".into(),
        executors_dir: platform::paths::default_executors_dir().to_string_lossy().into(),
        snapshot_dir: platform::paths::default_snapshot_dir().to_string_lossy().into(),
        snapshot_retention: 5,
        trait_refresh_cycles: 10,
        result_cache_cycles: 5,
        dry_run: false,
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
            "run" => {
                let mut module = None;
                let mut dry_run = false;
                let mut j = 1;
                while j < positional.len() {
                    match positional[j].as_str() {
                        "--module" => { j += 1; module = Some(positional.get(j).cloned().context("--module requires a value")?); }
                        "--dry-run" => { dry_run = true; }
                        _ => bail!("unknown run flag: {}", positional[j]),
                    }
                    j += 1;
                }
                Commands::Run { module, dry_run }
            }
            "register" => {
                let mut token = None;
                let mut j = 1;
                while j < positional.len() {
                    match positional[j].as_str() {
                        "--token" => { j += 1; token = Some(positional.get(j).cloned().context("--token requires a value")?); }
                        _ => bail!("unknown register flag: {}", positional[j]),
                    }
                    j += 1;
                }
                Commands::Register { token: token.context("register requires --token")? }
            }
            "bootstrap" => {
                let mut server = None;
                let mut token = None;
                let mut install_service = false;
                let mut grpc_port = "1530".to_string();
                let mut j = 1;
                while j < positional.len() {
                    match positional[j].as_str() {
                        "--server" => { j += 1; server = Some(positional.get(j).cloned().context("--server requires a value")?); }
                        "--token" => { j += 1; token = Some(positional.get(j).cloned().context("--token requires a value")?); }
                        "--install-service" => { install_service = true; }
                        "--grpc-port" => { j += 1; grpc_port = positional.get(j).cloned().context("--grpc-port requires a value")?; }
                        _ => bail!("unknown bootstrap flag: {}", positional[j]),
                    }
                    j += 1;
                }
                Commands::Bootstrap {
                    server: server.context("bootstrap requires --server")?,
                    token,
                    install_service,
                    grpc_port,
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
    --api-addr <URL>              Server REST API address [default: http://localhost:8181]
    --executors-dir <PATH>        Custom executor scripts [default: {}]
    --snapshot-dir <PATH>         File snapshots directory [default: {}]
    --snapshot-retention <N>      Max snapshots per file [default: 5]
    --trait-refresh-cycles <N>    Trait refresh interval in cycles [default: 10]
    --result-cache-cycles <N>     Result cache lifetime in cycles [default: 5]
    --dry-run                     Preview mode

COMMANDS:
    run         Run a single convergence cycle and exit
    register    Register with the server (--token <TOKEN>)
    bootstrap   Bootstrap agent (--server <URL> [--token <TOKEN>] [--install-service])
    version     Print agent version
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
            })
            .collect();

        if let Err(e) = client.report_result(&run_id, proto_results).await {
            warn!(error = %e, "failed to report results to server");
        }
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
            let s = r.action.to_string();
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
        }),
        state_store,
        server_pubkey_pem: load_server_pubkey(&cli.data_dir),
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

#[cfg(windows)]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
    data_dir: &std::path::Path,
) -> Result<String> {
    bootstrap::windows_service::install_service(bootstrap_server, grpc_port, data_dir).await
}

#[cfg(not(any(target_os = "linux", target_os = "macos", target_os = "freebsd", target_os = "netbsd", target_os = "openbsd", windows)))]
async fn install_platform_service(
    _bootstrap_server: &str,
    _grpc_port: &str,
    _data_dir: &std::path::Path,
) -> Result<String> {
    bail!("service installation is not supported on this platform")
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

        Some(Commands::Register { token }) => {
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

            let resp = client.register(&hostname, &ip_addr, &pub_pem, token).await?;

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
            };
            let resp = bootstrap::client::register(bootstrap_server, &req).await?;

            // Save TLS artifacts and thrall UUID.
            auth::keypair::save_tls_key(&cli.data_dir, &tls_key)?;
            auth::keypair::save_tls_cert(&cli.data_dir, &resp.signed_cert_pem)?;
            auth::keypair::save_ca_cert(&cli.data_dir, &resp.ca_cert_pem)?;
            save_thrall_id(&cli.data_dir, &resp.thrall_id)?;

            // Persist server address so subsequent runs don't need --server.
            let grpc_addr = format!("{}:{}", bootstrap_server.trim_start_matches("http://").trim_start_matches("https://").split(':').next().unwrap_or(bootstrap_server), grpc_port);
            if let Err(e) = config::save(&cli.data_dir, &[("server", &grpc_addr), ("api_addr", bootstrap_server)]) {
                warn!(error = %e, "failed to save config");
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

        Some(Commands::Run { module, dry_run: run_dry_run }) => {
            run_once(
                &cli,
                module.clone(),
                cli.dry_run || *run_dry_run,
            )
            .await
        }

        None => {
            // Daemon mode — persistent loop with backoff + stream mode.
            run_daemon(&cli).await
        }
    }
}
