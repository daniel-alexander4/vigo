// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

use vigo::auth;
use vigo::bootstrap;
use vigo::checkin;
use vigo::config;
use vigo::executors;
use vigo::multicall;
use vigo::platform;
use vigo::policy;
use vigo::update;
use vigo::proto;
use vigo::runner;
use vigo::snapshots;
use vigo::state;
use vigo::swarm;
use vigo::traits;

use anyhow::{bail, Context, Result};
use std::collections::HashSet;
use std::path::PathBuf;
use tracing::{info, warn};

/// Version injected at build time.
const VERSION: &str = env!("CARGO_PKG_VERSION");

/// Greppable build marker. The bytes live inside a fixed-size byte array
/// (not behind a `&str` pointer) so they can't be detached from the static
/// by linker `--gc-sections`. The array is placed in its own
/// `.vigo_version` section, marked `#[used]` and `#[no_mangle]`, and
/// referenced from `main` via `black_box` so even fat LTO can't prove
/// it dead. Cross-arch tooling extracts the version with
/// `grep -aoE 'VIGO-BUILD-VERSION=[0-9.]+' <binary>` — no execution,
/// works for any target architecture from any host.
#[used]
#[no_mangle]
#[link_section = ".vigo_version"]
pub static VIGO_BUILD_VERSION: [u8; 64] = {
    let prefix = b"VIGO-BUILD-VERSION=";
    let version = env!("CARGO_PKG_VERSION").as_bytes();
    let mut buf = [0u8; 64];
    let mut i = 0;
    while i < prefix.len() {
        buf[i] = prefix[i];
        i += 1;
    }
    let mut j = 0;
    while j < version.len() {
        buf[prefix.len() + j] = version[j];
        j += 1;
    }
    buf
};

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
    /// User-facing gitback inspection and recovery tree.
    /// Pure local state reads; does not start the daemon or touch the
    /// network. Dispatched from `vigo swarm gitback ...`.
    Gitback {
        args: Vec<String>,
    },
    /// User-facing lockbox key management + encrypted-directory verbs.
    /// Dispatched from `vigo swarm lockbox ...`. Week 1 verbs are pure
    /// user-home file operations; later weeks add session-helper and
    /// transport paths.
    Lockbox {
        args: Vec<String>,
    },
    /// Hidden: lockbox session helper daemon. Invoked by
    /// `vigo swarm lockbox unlock` via `current_exe()` + `setsid()`;
    /// never by a human. Reads the unwrapped age identity from stdin,
    /// binds its Unix socket, serves DECRYPT/STATUS/LOCK until timeout
    /// or parent-PID exit.
    LockboxSessionDaemon {
        args: Vec<String>,
    },
    /// Per-user fleet-identity verbs (ADR-014). Dispatched from
    /// `vigo swarm puddle ...`. State lives at ~/.vigo-puddle/.
    /// Verbs: init / pair / join / unlock / lock / pubkey / members /
    /// rotate-passphrase / status.
    SelfFleet {
        args: Vec<String>,
    },
    /// Curator artifact-registry CLI (ADR-024). Dispatched from
    /// `vigo swarm curator ...`. Read verbs (status/list/inspect/
    /// versions/resolve/pull) ship in slice 4b; mutating verbs (push/
    /// tag/untag/set-recipients/set-target/rm/prune/gc/stale/purge)
    /// ship in slice 4c and need `curator: true` plus an unlocked
    /// puddle.
    Curator {
        args: Vec<String>,
    },
    /// Poolq ordered-log CLI (ADR-029). Dispatched from `vigo swarm poolq ...`.
    /// Read verbs (topics/subscribe/read/tail/seek/offset/status) need only
    /// the agent's bootstrap TLS; `post` additionally needs `poolq: true`
    /// on the usercrate and an unlocked per-user puddle.
    Poolq {
        args: Vec<String>,
    },
    /// Hidden: puddle session helper daemon. Invoked by `unlock`
    /// (and the join flow) via `current_exe()` + `setsid()`; never
    /// by a human. Reads the 32-byte Ed25519 key from stdin, binds
    /// its Unix socket, serves SIGN / STATUS / PUBKEY / LOCK until
    /// timeout or parent-PID exit.
    SelfFleetSessionDaemon {
        args: Vec<String>,
    },
    /// Hidden: service-account puddle helper daemon (ADR-025 Slice 1b).
    /// Started by the agent at boot if `/srv/vigo/puddle/identity.wrapped`
    /// exists; unwraps the service-account Ed25519 identity using the
    /// HKDF-derived wrap key from the secrets master key, binds
    /// `/var/run/vigo/puddle.sock` (0600, SO_PEERCRED-gated to the
    /// wrap-file owner uid), serves SIGN / PUBKEY / STATUS. No idle
    /// timeout; reloads on identity / salt / master-key mtime change.
    ServicePuddleDaemon {
        args: Vec<String>,
    },
    /// Hidden: fleet-wide swarm-cache cleanup. Dispatched by the
    /// server when an admin runs `vigocli swarm cleanup`. Walks the
    /// blob cache, removes dangling `files/<label>` symlinks (target
    /// missing), removes `.partial/` dirs whose final blob has
    /// already been assembled. Idempotent; safe to re-run.
    SwarmApplyCleanup,
    /// Read-only diagnostic verbs for the swarm substrate (0.41.1+):
    /// `vigo swarm status`, `vigo swarm seeds {list,show}`,
    /// `vigo swarm peers list`. Reads /var/lib/vigo/swarm/{state,
    /// manifest,peers}.json (mode 0644). No sudo required.
    SwarmDiag {
        args: Vec<String>,
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
            "swarm" => match positional.get(1).map(|s| s.as_str()) {
                Some("gitback") => Commands::Gitback { args: positional[2..].to_vec() },
                Some("lockbox") => Commands::Lockbox { args: positional[2..].to_vec() },
                Some("puddle") => Commands::SelfFleet { args: positional[2..].to_vec() },
                Some("curator") => Commands::Curator { args: positional[2..].to_vec() },
                Some("poolq") => Commands::Poolq { args: positional[2..].to_vec() },
                // Read-only diagnostic verbs (0.41.1+): substrate
                // status, seed inventory, peer table, hostdox posture.
                // Read against /var/lib/vigo/swarm/{state,manifest,
                // peers}.json and the /etc/hosts managed block.
                Some("status") | Some("seeds") | Some("peers") | Some("hostdox") => {
                    Commands::SwarmDiag { args: positional[1..].to_vec() }
                }
                // Help routes through the diag dispatcher so the
                // user sees the full envoy-side verb tree.
                Some("--help") | Some("-h") | Some("help") | None => {
                    Commands::SwarmDiag { args: positional[1..].to_vec() }
                }
                // Hidden: server-dispatched cleanup verb. Mirrors the
                // `_apply-blacklist` pattern (gitback/lockbox), but
                // operates on the substrate-shared blob cache rather
                // than per-substrate state. Not advertised in --help.
                Some("_apply-cleanup") => Commands::SwarmApplyCleanup,
                Some(other) => bail!(
                    "unknown swarm subcommand: {other}\n\
                     run `vigo swarm --help` for usage"
                ),
            },
            "lockbox-session-daemon" => {
                Commands::LockboxSessionDaemon { args: positional[1..].to_vec() }
            }
            "puddle-session-daemon" => {
                Commands::SelfFleetSessionDaemon { args: positional[1..].to_vec() }
            }
            "service-puddle-daemon" => {
                Commands::ServicePuddleDaemon { args: positional[1..].to_vec() }
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
    eprintln!("vigo — Vigo configuration management agent

USAGE:
    vigo [FLAGS] [COMMAND]

FLAGS:
    --server <ADDR>               Server gRPC address [default: localhost:1530]
    --ca-file <PATH>              CA certificate file
    --cert-file <PATH>            Client certificate file
    --key-file <PATH>             Client key file
    --log-level <LEVEL>           Log level [default: info]
    --api-addr <URL>              Server REST API address [default: https://localhost:8443]
    --executors-dir <PATH>        Custom executor scripts [default: {1}]
    --snapshot-dir <PATH>         File snapshots directory [default: {2}]
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
    swarm gitback     Inspect + recover gitback mirrors on this envoy
                          list | show | log | branches | peers | recover | recover-all
    swarm lockbox     Manage lockbox — per-user encrypted P2P directory
                          init | enroll | status | unlock | lock | encrypt | decrypt | forget | reencrypt
    swarm curator     P2P artifact registry (ADR-024)
                          status | list | inspect | versions | resolve | pull
    swarm poolq       P2P ordered-log queue (ADR-029)
                          post | topics | subscribe | read | tail | seek | offset | status

With no command, runs as a daemon.

Settings are resolved in order: CLI flags > {0}/config > defaults.
The config file is written automatically during register/bootstrap.",
        data_dir.display(), exec_dir.display(), snap_dir.display());
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

/// Stop the agent daemon service so `vigo run` can acquire the convergence lock.
/// The service is restarted after convergence by `ensure_daemon_running()`.
fn stop_daemon_service() {
    use std::process::Command;

    #[cfg(unix)]
    {
        if std::path::Path::new("/run/systemd/system").exists() {
            match Command::new("systemctl").args(["stop", "vigo-envoy"]).status() {
                Ok(s) if s.success() => info!("vigo-envoy service stopped"),
                Ok(s) => warn!("failed to stop vigo-envoy (exit {})", s),
                Err(e) => warn!("failed to stop vigo-envoy: {}", e),
            }
        } else if std::path::Path::new("/Library/LaunchDaemons/com.vigo.envoy.plist").exists() {
            let _ = Command::new("launchctl").args(["stop", "com.vigo.envoy"]).status();
        }
    }

    #[cfg(windows)]
    {
        let _ = Command::new("sc").args(["stop", "vigo-envoy"]).status();
    }
}

/// Ensure the agent daemon service is running. Called after `vigo run`
/// so the daemon resumes if it was stopped for manual testing.
fn ensure_daemon_running() {
    use std::process::Command;

    #[cfg(unix)]
    {
        if std::path::Path::new("/run/systemd/system").exists() {
            // systemd
            let running = Command::new("systemctl")
                .args(["is-active", "--quiet", "vigo-envoy"])
                .status()
                .map(|s| s.success())
                .unwrap_or(false);
            if !running {
                info!("starting vigo-envoy daemon");
                match Command::new("systemctl").args(["start", "vigo-envoy"]).status() {
                    Ok(s) if s.success() => info!("vigo-envoy daemon started"),
                    Ok(s) => warn!("failed to start vigo-envoy (exit {})", s),
                    Err(e) => warn!("failed to start vigo-envoy: {}", e),
                }
            }
        } else if std::path::Path::new("/Library/LaunchDaemons/com.vigo.envoy.plist").exists() {
            // launchd (macOS)
            let running = Command::new("launchctl")
                .args(["list", "com.vigo.envoy"])
                .status()
                .map(|s| s.success())
                .unwrap_or(false);
            if !running {
                info!("starting vigo-envoy daemon");
                let _ = Command::new("launchctl")
                    .args(["load", "-w", "/Library/LaunchDaemons/com.vigo.envoy.plist"])
                    .status();
            }
        } else if std::path::Path::new("/etc/rc.d/vigo").exists()
            || std::path::Path::new("/usr/local/etc/rc.d/vigo").exists()
        {
            // BSD rc.d
            let running = Command::new("service")
                .args(["vigo", "status"])
                .status()
                .map(|s| s.success())
                .unwrap_or(false);
            if !running {
                info!("starting vigo daemon");
                let _ = Command::new("service").args(["vigo", "start"]).status();
            }
        }
        // No known service manager — skip silently.
    }

    #[cfg(windows)]
    {
        let running = Command::new("sc.exe")
            .args(["query", "vigo-envoy"])
            .output()
            .map(|o| String::from_utf8_lossy(&o.stdout).contains("RUNNING"))
            .unwrap_or(false);
        if !running {
            info!("starting vigo-envoy service");
            let _ = Command::new("sc.exe").args(["start", "vigo-envoy"]).status();
        }
    }
}

/// Read envoy UUID from <data-dir>/envoy.uuid.
fn load_envoy_id(data_dir: &std::path::Path) -> Result<String> {
    let path = data_dir.join("envoy.uuid");
    let data = std::fs::read_to_string(&path)
        .with_context(|| format!("read envoy UUID from {} (have you run 'vigo register'?)", path.display()))?;
    Ok(data.trim().to_string())
}

/// Write envoy UUID to <data-dir>/envoy.uuid.
fn save_envoy_id(data_dir: &std::path::Path, id: &str) -> Result<()> {
    let path = data_dir.join("envoy.uuid");
    std::fs::write(&path, format!("{}\n", id))
        .with_context(|| format!("write envoy UUID to {}", path.display()))?;
    // Set permissions to 0600
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))?;
    }
    Ok(())
}

/// Save bolt address to <data-dir>/bolt.addr.
fn save_bolt_addr(data_dir: &std::path::Path, addr: &str) -> Result<()> {
    let path = data_dir.join("bolt.addr");
    if addr.is_empty() {
        let _ = std::fs::remove_file(&path);
        return Ok(());
    }
    std::fs::write(&path, format!("{}\n", addr))?;
    Ok(())
}

/// Load bolt address. Returns empty string if not set.
fn load_bolt_addr(data_dir: &std::path::Path) -> String {
    let path = data_dir.join("bolt.addr");
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

/// Classify a check-in error into a short human-readable reason and a
/// "scary" flag used for color-coding stderr output.
///
/// Transport-layer failures (connection refused, TLS handshake, DNS,
/// timeout) are genuinely scary — the operator's network or the server's
/// listener is broken and retrying might help. gRPC application errors
/// (codes.Internal, codes.PermissionDenied, codes.NotFound, etc.) mean
/// the server is reachable but rejected the request for a specific
/// reason that retrying won't fix; the operator needs to see the code
/// and message, and the presentation should be less alarming.
///
/// Returns `(short_reason, scary)` where:
///   - `short_reason` is a single-line message suitable for stderr
///   - `scary` is `true` for transport failures, `false` for gRPC
///     status rejections. Used to pick the color of the stderr line.
fn classify_checkin_error(err: &anyhow::Error) -> (String, bool) {
    // Walk the anyhow chain looking for a concrete tonic error type.
    // tonic::Status is the application-level error (server returned a
    // status code). tonic::transport::Error is the network/TLS layer.
    // The context wrap at checkin/client.rs:365 puts these underneath
    // the "check-in RPC" string, so we have to downcast.
    for cause in err.chain() {
        if let Some(status) = cause.downcast_ref::<tonic::Status>() {
            return (
                format!(
                    "Server rejected check-in: {} — {}",
                    status.code(),
                    status.message()
                ),
                false,
            );
        }
        if cause.downcast_ref::<tonic::transport::Error>().is_some() {
            return (format!("Server unreachable: {}", cause), true);
        }
    }
    // Fallback: unknown error type, treat as transport failure so
    // the operator still sees the loud signal.
    (format!("Server unreachable: {}", err), true)
}

/// Run a single convergence cycle with color-coded trace output.
async fn run_once(
    cli: &Cli,
    module_filter: Option<String>,
    dry_run: bool,
) -> Result<()> {
    let (_, priv_key) = auth::keypair::load_or_generate(&cli.data_dir)?;
    let envoy_id = load_envoy_id(&cli.data_dir)?;

    let (ca, cert, key) = resolve_tls_paths(cli, &cli.data_dir);

    // Use bolt address if the receiving bolt routed us elsewhere.
    let addr = {
        let bolt = load_bolt_addr(&cli.data_dir);
        if bolt.is_empty() {
            cli.server.clone()
        } else {
            info!(bolt_addr = %bolt, "using bolt address from enrollment routing");
            bolt
        }
    };

    // Try server first. If unreachable, fall back to cached policy.
    let server_result = async {
        let mut client = checkin::client::Client::connect(
            &addr,
            ca.as_deref(),
            cert.as_deref(),
            key.as_deref(),
            &envoy_id,
            priv_key,
        )
        .await?;

        client.set_agent_version(VERSION);

        // Collect traits before check-in so the server can filter modules by OS/arch.
        let pre_traits = traits::collect_all(&traits::default_collectors()).await;
        let traits_json = serde_json::to_vec(&pre_traits).ok();

        info!(server = %addr, envoy_id = %envoy_id, version = VERSION, "starting check-in");
        let resp = client.check_in("", traits_json, 0, None).await?;
        Ok::<_, anyhow::Error>((client, resp, pre_traits))
    }
    .await;

    let offline = server_result.is_err();
    let (mut maybe_client, collected_traits, modules, bundle_for_trace, force_update);

    if let Ok((mut client, mut server_resp, pre_traits)) = server_result {
        for w in &server_resp.warnings {
            warn!("server warning: {}", w);
        }

        if server_resp.no_change {
            info!(source = "server", "server reports no change");
            return Ok(());
        }

        force_update = server_resp.force_update;

        // Use traits already collected before check-in.
        collected_traits = pre_traits;

        // Resolve placeholder secrets before eval_bundle. See
        // policy::secrets for the marker convention; without this
        // substitution, eval_bundle would feed placeholder strings
        // into executors.
        if let Some(bundle) = server_resp.policy_bundle.as_mut() {
            policy::secrets::resolve_and_substitute(
                bundle,
                policy::secrets::global_cache(),
                &mut client,
            )
            .await
            .context("resolving placeholder secrets")?;
        }
        maybe_client = Some(client);

        // Get modules from either policy bundle (agent-side eval) or compiled modules.
        let (m, b) = if let Some(ref bundle) = server_resp.policy_bundle {
            info!(
                source = "server",
                policy_version = bundle.policy_version,
                modules = bundle.configcrates.len(),
                "applying policy"
            );

            match policy::eval::eval_bundle(Some(bundle), &collected_traits) {
                Ok(mods) => (mods, Some(bundle.clone())),
                Err(e) => {
                    warn!("policy evaluation failed, running with compiled modules: {}", e);
                    (server_resp.configcrates.clone(), server_resp.policy_bundle.clone())
                }
            }
        } else {
            info!(
                source = "server",
                modules = server_resp.configcrates.len(),
                "applying compiled modules"
            );
            (server_resp.configcrates, server_resp.policy_bundle)
        };
        modules = m;
        bundle_for_trace = b;
    } else {
        // Check-in RPC failed — distinguish transport failures from
        // application-level gRPC errors. Transport failures ("server
        // unreachable") are usually network / TLS / DNS issues that
        // the operator can retry. gRPC application errors (codes.*) are
        // server-side rejections that usually mean a config or data bug
        // on the server, and retry alone won't fix them — the operator
        // needs to see the actual status code and message.
        let err = server_result.err().unwrap();
        maybe_client = None;
        force_update = false;

        let (short_reason, scary) = classify_checkin_error(&err);

        let state_dir = platform::paths::default_state_dir();
        let store = match state::StateStore::open(&state_dir) {
            Ok(s) => s,
            Err(_) => {
                // No state store — can't fall back.
                return Err(err).context(format!("{short_reason}; no cached policy available"));
            }
        };

        let rtxn = store.read_txn()?;
        let cached = store.policy.load(&rtxn)?;
        let cached_traits = store.traits.load_any(&rtxn).ok().flatten();
        drop(rtxn);

        let (version, bundle) = match cached {
            Some(vb) => vb,
            None => {
                return Err(err).context(format!("{short_reason}; no cached policy available"));
            }
        };

        // Single eye-catching line to stderr so an operator running
        // `vigo run` manually can't miss that the server leg failed.
        // Red for genuine unreachability, yellow for gRPC rejections
        // so the operator can tell "your network is broken" from
        // "the server rejected this envoy's check-in".
        let color = if scary {
            "\x1b[1m\x1b[38;2;243;139;168m" // catppuccin red
        } else {
            "\x1b[1m\x1b[38;2;249;226;175m" // catppuccin yellow
        };
        eprintln!(
            "{}{} — converging from cached policy (v{})\x1b[0m",
            color, short_reason, version
        );
        warn!(error = %err, version = version, source = "cache", "falling back to cached policy");
        info!(
            source = "cache",
            policy_version = version,
            "applying cached policy"
        );

        collected_traits = if let Some(tb) = cached_traits {
            serde_json::from_slice(&tb).unwrap_or_default()
        } else {
            // No cached traits — collect fresh ones locally.
            traits::collect_all(&traits::default_collectors()).await
        };

        // Offline-cache limitation: substitute placeholder secrets from
        // the in-memory cache. If the cache doesn't have them (cold
        // restart with server unreachable), fail with a clear message
        // rather than writing placeholders into config files.
        let mut bundle = bundle;
        let refs = policy::secrets::collect_refs_in_bundle_pub(&bundle);
        let cache = policy::secrets::global_cache();
        let missing = cache.missing_refs_pub(refs.iter().map(|s| s.as_str()));
        if !missing.is_empty() {
            bail!(
                "{short_reason} and cached policy references {} unresolved secret(s) (offline cache empty); awaiting server contact before applying",
                missing.len()
            );
        }
        policy::secrets::substitute_in_bundle_pub(&mut bundle, cache)
            .context("substituting placeholder secrets in cached bundle")?;

        match policy::eval::eval_bundle(Some(&bundle), &collected_traits) {
            Ok(mods) => {
                modules = mods;
                bundle_for_trace = Some(bundle);
            }
            Err(e) => {
                bail!("{short_reason} and cached policy evaluation failed: {}", e);
            }
        }
    }

    // Self-update: if the server says our binary is outdated, download the
    // new one and re-exec before converging. This ensures `vigo run` always
    // uses the latest binary without requiring a separate re-bootstrap step.
    if force_update && !dry_run && !offline {
        let tls_certs = if auth::keypair::has_bootstrap_certs(&cli.data_dir) {
            Some(update::TlsCerts {
                ca_cert: auth::keypair::ca_cert_path(&cli.data_dir).to_string_lossy().to_string(),
                client_cert: auth::keypair::tls_cert_path(&cli.data_dir).to_string_lossy().to_string(),
                client_key: auth::keypair::tls_key_path(&cli.data_dir).to_string_lossy().to_string(),
            })
        } else {
            None
        };
        info!("server requested update, downloading latest binary before convergence");
        update::force_self_update(&cli.api_addr, VERSION, tls_certs.as_ref()).await;
        // If we get here, exec() failed or update wasn't available.
        // Continue with convergence on current binary.
    }

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

    // Acquire convergence lock — stop the daemon if it holds it.
    let _lock = match runner::lock::ConvergenceLock::try_acquire(&cli.data_dir)? {
        Some(guard) => guard,
        None => {
            // Daemon holds the lock — stop the service to release it.
            info!("daemon is converging — stopping vigo-envoy service");
            stop_daemon_service();
            let deadline = std::time::Instant::now() + std::time::Duration::from_secs(30);
            loop {
                std::thread::sleep(std::time::Duration::from_millis(250));
                if let Some(guard) = runner::lock::ConvergenceLock::try_acquire(&cli.data_dir)? {
                    break guard;
                }
                if std::time::Instant::now() >= deadline {
                    anyhow::bail!("timed out waiting for daemon to stop");
                }
            }
        }
    };

    // Run convergence.
    let run_id = uuid::Uuid::new_v4().to_string();
    let mut cache = runner::cache::ResultCache::new(cli.result_cache_cycles);

    let opts = runner::RunOptions {
        dry_run,
        module_filter,
        run_id: run_id.clone(),
        stream_edit_allowed_paths: Vec::new(), // local mode: no server-side restrictions
        stream_edit_timeout_secs: 10,
        data_dir: None, // manual run doesn't need preempt checks on itself
    };

    let mut pkg_breaker = runner::pkg_breaker::PackageBreaker::new();
    let results = runner::converge(&modules, &executors, &mut cache, &opts, &collected_traits, &mut pkg_breaker);

    // Print color-coded trace output to stderr.
    print_trace(&bundle_for_trace, &results, dry_run);

    if offline {
        // End-of-output banner: unmissable yellow reminder that this
        // run used cached policy and nothing was reported back.
        // Operators running `vigo run` manually to diagnose a failure
        // need to see this at the bottom of the output, not just
        // inferred from the top-of-run warning that scrolled past.
        let version_note = bundle_for_trace
            .as_ref()
            .map(|b| format!("v{}", b.policy_version))
            .unwrap_or_else(|| "unknown version".to_string());
        eprintln!();
        eprintln!(
            "\x1b[1m\x1b[38;2;249;226;175m━━ Converged from cached policy ({}) — results not reported to server\x1b[0m",
            version_note
        );
        eprintln!(
            "\x1b[38;2;249;226;175m   Fix server connectivity and re-run to restore live convergence.\x1b[0m"
        );
    }

    // Report results back to server (only if connected).
    if !dry_run && !offline {
        if let Some(ref mut client) = maybe_client {
            let proto_results: Vec<proto::ConfigcrateResult> = results
                .iter()
                .map(|r| proto::ConfigcrateResult {
                    configcrate_name: r.configcrate_name.clone(),
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

            match tokio::time::timeout(
                std::time::Duration::from_secs(10),
                client.report_result(&run_id, proto_results),
            ).await {
                Ok(Ok(_)) => {}
                Ok(Err(e)) => warn!(error = %e, "failed to report results to server"),
                Err(_) => warn!("report_result timed out after 10s"),
            }
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

    // Catppuccin Mocha — 24-bit RGB so colors look identical on every terminal.
    const BOLD: &str = "\x1b[1m";
    const DIM: &str = "\x1b[38;2;127;132;156m";      // Overlay1 #7f849c
    const RED: &str = "\x1b[38;2;243;139;168m";       // Red      #f38ba8
    const GREEN: &str = "\x1b[38;2;166;227;161m";     // Green    #a6e3a1
    const YELLOW: &str = "\x1b[38;2;249;226;175m";    // Yellow   #f9e2af
    const CYAN: &str = "\x1b[38;2;137;220;235m";      // Sky      #89dceb
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
        let mod_names: Vec<&str> = b.configcrates.iter().map(|m| m.name.as_str()).collect();
        if !mod_names.is_empty() {
            let _ = writeln!(out, "{BOLD}Modules:{RESET}     {}", mod_names.join(", "));
        }
        let _ = writeln!(out);
    }

    // Print results grouped by module.
    let mut cur_module = "";
    for r in results {
        if r.configcrate_name != cur_module {
            cur_module = &r.configcrate_name;
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
                executors::Action::Blocked => YELLOW.to_string(),
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
    // Service-account puddle helper (ADR-025 Slice 1b). No-op on
    // envoys that aren't co-located with a vigosrv (the wrap file
    // won't exist). Idempotent: skipped if a live helper is already
    // bound to /var/run/vigo/puddle.sock. Spawned detached so it
    // outlives every agent restart and serves SIGN / PUBKEY to
    // root-side helpers that need the service-account identity
    // without re-deriving the wrap key per call.
    vigo::swarm::puddle::service_session::try_spawn_helper();

    // Self-heal the systemd unit file at startup so envoys whose
    // binary was deployed via a path that bypassed `update::apply`
    // (manual `scp` + `systemctl restart`, package install, image
    // build) still end up with the current template. Best-effort:
    // we do NOT fail daemon startup on any error here — the agent
    // can still run with a stale unit, the next restart just won't
    // pick up new fields. Linux+systemd only; the helper internally
    // skips on platforms without the unit path.
    //
    // Why here instead of just in `update::apply`: the parity gap
    // surfaced on 2026-04-26 when girlslaptop's vigo-envoy.service
    // shipped without `LimitNOFILE=65536`, the agent hit EMFILE in
    // its swarm accept loop, and 836 GiB of syslog filled the
    // disk. Update path *did* have the refresh, but the host's
    // upgrade pathway didn't route through it. Calling on every
    // daemon start closes the gap regardless of deployment route.
    #[cfg(target_os = "linux")]
    {
        match bootstrap::systemd::refresh_unit_file_if_changed().await {
            Ok(true) => info!("systemd unit refreshed on startup"),
            Ok(false) => {} // already current, nothing to log
            Err(e) => warn!(error = %e, "systemd unit refresh failed (non-fatal)"),
        }
    }

    let (_, priv_key) = auth::keypair::load_or_generate(&cli.data_dir)?;
    let envoy_id = load_envoy_id(&cli.data_dir)?;
    let (ca, cert, key) = resolve_tls_paths(cli, &cli.data_dir);

    // Use bolt address if the receiving bolt routed us elsewhere.
    let addr = {
        let bolt = load_bolt_addr(&cli.data_dir);
        if bolt.is_empty() {
            cli.server.clone()
        } else {
            info!(bolt_addr = %bolt, "using bolt address from enrollment routing");
            bolt
        }
    };

    let mut client = checkin::client::Client::connect(
        &addr,
        ca.as_deref(),
        cert.as_deref(),
        key.as_deref(),
        &envoy_id,
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
        envoy_id = %envoy_id,
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
    let state_store: Option<std::sync::Arc<state::StateStore>> = match state::StateStore::open(&state_dir) {
        Ok(s) => {
            info!(path = %state_dir.display(), "persistent state store ready");
            Some(std::sync::Arc::new(s))
        }
        Err(e) => {
            warn!(error = %e, "could not open state store, offline convergence disabled");
            None
        }
    };

    // Wrap reversible executors with the persistent reversal store so the
    // `revert:` lever can capture/restore prior values and run `on_revert:`
    // inverses. No-op when the state store is unavailable.
    runner::reversal::wrap_reversible(&mut executors, state_store.clone());

    // Configure daemon loop.
    let traits_cache_dir = cli.data_dir.join("traits").to_string_lossy().to_string();
    // Create the peer map and manifest early so they can be shared between LoopConfig,
    // the swarm peer server, and multicast discovery.
    let swarm_base = platform::paths::default_swarm_base();
    let shared_peer_map = std::sync::Arc::new(swarm::peermap::PeerMap::load(&swarm_base.join("peers.json")));
    let shared_manifest = swarm::manifest::Manifest::load(&swarm_base);

    // Shared announce signal between the download scheduler and the multicast
    // announcer. Lets the scheduler fire an immediate announce on the first
    // chunk of a fresh download (closing the discovery gap that used to keep
    // small-blob LAN downloads from ever trading chunks peer-to-peer) and
    // lets the announcer switch from 5-second idle cadence to 1-second
    // active cadence while any download is in flight.
    let announce_signal = swarm::AnnounceSignal::new();

    // Clean up any stale ephemeral SSH keys left by crashed Scrier sessions.
    vigo::tunnel::sshauth::cleanup_stale_keys();

    // Gitback claims handle: shared between the swarm manager (reader
    // via `.shared()`), the check-in handler (writer for server-
    // delivered claims), the swarm discovery listener (writer for
    // LAN-ingested claims), and the gitback reactor (reader through
    // the receiver). Constructed here, before swarm discovery spawns,
    // so all of the above can share the same Arc.
    let gitback_claims = vigo::swarm::gitback::receiver::ClaimsHandle::new();

    // Gitback live on/off switch. Initial value comes from the cached
    // policy bundle; the check-in loop re-reads after every accepted
    // bundle and calls `set` to propagate flips without a restart. All
    // gitback consumers (discovery announcer, peer server handler,
    // reactor) consult this gate at their natural sample points.
    let gitback_gate = vigo::swarm::gitback::Gate::new(
        vigo::swarm::gitback::gitback_enabled_for_envoy(state_store.as_deref()),
    );

    // Gitback LAN peer table + local info + pending claims. Always
    // constructed so the gate can flip at runtime without restarting
    // the agent; the structures are inert while the gate is off (no
    // writes to the peer table, no GITBACK / GITBACKCLAIM lines
    // emitted, no `/gitback/push` accepted). Hostname read is a
    // single cheap syscall so we do it unconditionally.
    let gitback_peer_table = std::sync::Arc::new(vigo::swarm::gitback::peer_table::PeerTable::new());
    let gitback_pending_claims =
        std::sync::Arc::new(vigo::swarm::gitback::pending_claims::PendingClaims::new());
    // Load the envoy's Ed25519 keypair for signing gitback multicast
    // announcements. The verifying key is embedded in every GITBACK line
    // so receivers can verify the trailing SIG without needing a
    // separate pubkey-distribution channel. Trust-on-first-use cache
    // guards against later pubkey swaps by the same hostname.
    let (gitback_verify_key, gitback_signing_key) =
        vigo::auth::keypair::load_or_generate(&cli.data_dir)?;
    let gitback_pubkey_hex = hex::encode(gitback_verify_key.to_bytes());
    let gitback_pubkey_cache = std::sync::Arc::new(
        vigo::swarm::gitback::peer_pubkey_cache::PeerPubkeyCache::load_or_empty(),
    );
    let gitback_local_info = {
        let hostname = std::fs::read_to_string("/etc/hostname")
            .unwrap_or_else(|_| "unknown".to_string())
            .trim()
            .to_string();
        vigo::swarm::gitback::peer_table::LocalInfo {
            hostname,
            users: vigo::swarm::gitback::peer_table::local_human_users(),
            tags: Vec::new(),
            pubkey_hex: gitback_pubkey_hex,
        }
    };
    let gitback_signing_key = std::sync::Arc::new(gitback_signing_key);

    // Spawn swarm peer server + multicast discovery for P2P blob distribution.
    // Build TLS certs for the swarm manager (shared with peer server).
    let swarm_tls = if auth::keypair::has_bootstrap_certs(&cli.data_dir) {
        Some(update::TlsCerts {
            ca_cert: auth::keypair::ca_cert_path(&cli.data_dir).to_string_lossy().to_string(),
            client_cert: auth::keypair::tls_cert_path(&cli.data_dir).to_string_lossy().to_string(),
            client_key: auth::keypair::tls_key_path(&cli.data_dir).to_string_lossy().to_string(),
        })
    } else {
        None
    };

    // Bootstrap the gitback peer-pubkey cache from the server. Closes
    // the TOFU first-contact race by pre-populating with authoritative
    // pubkeys before any GITBACK multicast is processed. One-shot at
    // startup + 15-min refresh; failures fall back to TOFU. Skipped if
    // we don't have mTLS certs yet (pre-enrollment).
    if let Some(ref tls) = swarm_tls {
        vigo::swarm::gitback::pubkey_bootstrap::spawn(
            gitback_pubkey_cache.clone(),
            cli.api_addr.clone(),
            tls.clone(),
            cancel.clone(),
        );
        // ADR-022: keep the fleet friendly-name map (~/.vigo-puddle name
        // claims aggregated server-side) cached locally so
        // `git-remote-gitback` can resolve `gitback://<name>/<repo>`
        // and `vigo swarm puddle name set` can run its collision check —
        // both off the check-in path. Fetched over mTLS sans token, same
        // as the pubkey bootstrap above.
        vigo::swarm::puddle::names_bootstrap::spawn(
            cli.api_addr.clone(),
            tls.clone(),
            cancel.clone(),
        );
    }

    if let Some(ref tls) = swarm_tls {
        match swarm::server::build_tls_acceptor(&tls.ca_cert, &tls.client_cert, &tls.client_key) {
            Ok(tls_acceptor) => {
                let swarm_port = config::load(&cli.data_dir)
                    .get("swarm_port")
                    .and_then(|v| v.parse().ok())
                    .unwrap_or(swarm::server::DEFAULT_PORT);
                let blobs_dir = platform::paths::default_blobs_dir();
                let bw_monitor = swarm::bandwidth::BandwidthMonitor::new();
                bw_monitor.spawn_sampler(cancel.clone(), 50);
                let peer_map = shared_peer_map.clone();

                // Peer server.
                let swarm_cancel = cancel.clone();
                let pm = peer_map.clone();
                let sm = shared_manifest.clone();
                // Gitback server-side context for cross-subnet
                // push (Phase 7). Constructed unconditionally; the
                // gate field is consulted by the `/gitback/push`
                // handler, which returns 403 while dormant.
                let gitback_srv_ctx = Some(swarm::server::GitbackServerCtx {
                    claims: gitback_claims.clone(),
                    manifest: shared_manifest.clone(),
                    local_hostname: gitback_local_info.hostname.clone(),
                    gate: gitback_gate.clone(),
                });
                tokio::spawn(async move {
                    if let Err(e) = swarm::server::run(
                        swarm_port, blobs_dir.clone(), tls_acceptor, bw_monitor, pm, sm, swarm_cancel, gitback_srv_ctx,
                    ).await {
                        warn!(error = %e, "swarm peer server exited with error");
                    }
                });

                // Multicast discovery.
                let cache = swarm::cache::BlobCache::new(platform::paths::default_blobs_dir());
                if let Ok(cache) = cache {
                    swarm::discovery::spawn(
                        peer_map,
                        std::sync::Arc::new(cache),
                        shared_manifest.clone(),
                        Some(tls.clone()),
                        swarm_port,
                        cancel.clone(),
                        announce_signal.clone(),
                        Some(gitback_peer_table.clone()),
                        Some(gitback_local_info.clone()),
                        Some(gitback_pending_claims.clone()),
                        Some(gitback_claims.clone()),
                        gitback_gate.clone(),
                        Some(gitback_signing_key.clone()),
                        Some(gitback_pubkey_cache.clone()),
                    );
                }
            }
            Err(e) => {
                warn!(error = %e, "failed to configure swarm peer TLS, peer serving disabled");
            }
        }
    }

    // Swarm download manager — long-lived coordinator for all P2P downloads.
    // `gitback_claims` was constructed above so swarm discovery could
    // share the same Arc for LAN claim ingest.
    let swarm_manager = swarm::manager::SwarmManager::spawn(
        shared_peer_map.clone(),
        shared_manifest.clone(),
        state_store.clone(),
        swarm_tls.clone(),
        cancel.clone(),
        Some(gitback_claims.shared()),
        announce_signal.clone(),
    );

    // Echo daemon — per-user LAN-only directory sync. Piggybacks on the
    // swarm peer HTTPS server for content transport; uses its own multicast
    // group/port for peer discovery. No server involvement.
    vigo::swarm::longdrawer::spawn(swarm_tls.clone(), cancel.clone());

    // Lockbox daemon — per-user always-encrypted P2P directory.
    // Separate multicast group (224.0.0.44:1533) and separate peer
    // table from echo; no shared state. HTTPS push/pull routes
    // ride the same swarm peer server on port 1531. TLS certs feed
    // the watcher+pusher+cross_subnet_peers unicast; without them the
    // daemon still runs in passive mode (discovery + serves pulls).
    vigo::swarm::lockbox::spawn(swarm_tls.clone(), cancel.clone());

    // Gitback subsystem — git-commit-triggered repo sync over swarm.
    // Pass both halves of the receiver wiring: the claims handle (for
    // the per-envoy wanted-labels set) and the in-memory swarm manifest
    // (for resolving labels to blob shas inside the receiver tick).
    //
    // The reactor is always spawned; its tick consults `gitback_gate`
    // and runs the publish/receive work only while the gate is on. The
    // check-in loop flips the gate after every accepted policy bundle
    // so operators can enable/disable the subsystem without restarting
    // the agent.
    vigo::swarm::gitback::spawn(
        swarm_tls.clone(),
        cancel.clone(),
        gitback_gate.clone(),
        gitback_pubkey_cache.clone(),
        gitback_peer_table.clone(),
        gitback_claims.clone(),
    );

    // Curator subsystem — P2P artifact registry (ADR-024). The helper
    // daemon at /var/run/vigo/curator.sock takes a `curator: true`
    // user's re-signed catalog entry + the blob, stages the blob into
    // the shared swarm cache, records a Manifest entry with the
    // artifact's target (so the filecast Manager distributes it), and
    // drops the entry as a *.pending sentinel; the sentinel reactor
    // promotes it to the canonical entry.json the `swarm_curator` trait
    // collector reads. Always spawned; per-host authorization
    // (`swarm.curator.enabled`) is enforced by `require_curator` at the
    // CLI front-ends (slice 4), per-user by the usercrate `curator:`
    // flag the daemon checks.
    vigo::swarm::curator::spawn(
        envoy_id.clone(),
        cancel.clone(),
        shared_manifest.clone(),
    );

    // hostdox reactor — maintains the agent-managed `/etc/hosts`
    // block from the swarm host index when the operator has set
    // `swarm.hostdox: true`. Always spawned; each tick reads the
    // gate from the cached policy bundle (default-OFF — see
    // `swarm::gates::hostdox_active`) and rewrites or removes the
    // block accordingly.
    vigo::swarm::hostdox::spawn(state_store.clone(), cancel.clone());

    let cfg = checkin::loop_impl::LoopConfig {
        trait_refresh_cycles: cli.trait_refresh_cycles,
        result_cache_cycles: cli.result_cache_cycles,
        traits_cache_dir: Some(traits_cache_dir),
        update_cfg: Some(checkin::loop_impl::UpdateConfig {
            api_addr: cli.api_addr.clone(),
            current_version: VERSION.to_string(),
            tls_certs: swarm_tls,
        }),
        state_store,
        server_pubkey_pem: load_server_pubkey(&cli.data_dir),
        data_dir: Some(cli.data_dir.clone()),
        peer_map: Some(shared_peer_map.clone()),
        manifest: Some(shared_manifest.clone()),
        swarm_manager: Some(swarm_manager),
        gitback_gate: Some(gitback_gate.clone()),
    };

    checkin::loop_impl::daemon_loop(cancel, &mut client, &executors, None, cfg).await
}

/// Get the preferred outbound IP (delegates to platform::net).
fn get_outbound_ip() -> String {
    vigo::platform::net::get_outbound_ip()
}

/// Platform-specific service installation.
#[cfg(target_os = "linux")]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
    bootstrap::systemd::install_service(bootstrap_server, grpc_port).await
}

#[cfg(target_os = "macos")]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
    bootstrap::launchd::install_service(bootstrap_server, grpc_port).await
}

#[cfg(any(target_os = "freebsd", target_os = "netbsd"))]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
    bootstrap::rc_d::install_service(bootstrap_server, grpc_port).await
}

#[cfg(target_os = "openbsd")]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
    bootstrap::rcctl::install_service(bootstrap_server, grpc_port).await
}

#[cfg(target_os = "illumos")]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
    bootstrap::smf::install_service(bootstrap_server, grpc_port).await
}

#[cfg(windows)]
async fn install_platform_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
    bootstrap::windows_service::install_service(bootstrap_server, grpc_port).await
}

#[cfg(not(any(target_os = "linux", target_os = "macos", target_os = "freebsd", target_os = "netbsd", target_os = "openbsd", target_os = "illumos", windows)))]
async fn install_platform_service(
    _bootstrap_server: &str,
    _grpc_port: &str,
) -> Result<String> {
    bail!("service installation is not supported on this platform")
}

/// Show local agent state: config, cached policy, pending results.
/// Reads only from local files and the LMDB state store — no server communication.
fn run_status(cli: &Cli) -> Result<()> {
    use std::time::{SystemTime, UNIX_EPOCH};

    // Load config.
    let cfg = config::load(&cli.data_dir);

    // Envoy ID.
    let envoy_id = match load_envoy_id(&cli.data_dir) {
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

    // Bolt address.
    let bolt = load_bolt_addr(&cli.data_dir);

    let data_dir = cli.data_dir.display();
    let state_dir = platform::paths::default_state_dir();

    println!("Envoy ID:        {}", envoy_id);
    println!("Server:           {}", server);
    if !bolt.is_empty() {
        println!("Bolt:          {}", bolt);
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
            let mod_count = bundle.configcrates.len();
            let mod_names: Vec<&str> = bundle.configcrates.iter().map(|m| m.name.as_str()).collect();
            let resource_count: usize = bundle.configcrates.iter().map(|m| m.resources.len()).sum();

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
            for m in &bundle.configcrates {
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

fn main() -> Result<()> {
    // Force the build-version marker to be observably live so LTO can't
    // prove it dead and the linker can't gc-section it. Zero runtime cost.
    std::hint::black_box(&VIGO_BUILD_VERSION);

    // Drift guard for bundle-signature verification. The hand-listed
    // SIGNED_FIELD_TAGS in checkin::verify must agree with
    // `(vigo.v1.signed)` annotations in protobuf/vigo.proto — if they
    // ever diverge, every bundle silently fails verify in production.
    // Catch the divergence here, at boot, with an actionable panic.
    checkin::verify::assert_signed_field_set_matches_proto();

    // ADR-015 multi-call binary dispatch: when invoked under a
    // recognized non-default argv[0] (e.g., as `git-remote-gitback` via
    // a symlink), branch to the corresponding mode without going
    // through clap. The check has to be before any arg parsing
    // because git invokes us with `<remote-name> <url>` positionals
    // that the regular CLI parser would reject. Lives behind argv[0]
    // canonicalization (strip dirs, strip `.exe`, lowercase) so a
    // package recipe symlink anywhere on PATH lands in the right
    // mode regardless of OS.
    //
    // Dispatch lands BEFORE we build the tokio runtime: the
    // git-remote-gitback body uses its own `current_thread` runtime
    // inside `tunnel()` to drive the bidirectional copy through
    // tokio async I/O (the fix for the AF_UNIX wakeup race that
    // hit 0.41.7-15 on the synchronous-recv path), and that nested
    // construction would panic if we were already inside the agent
    // multi-thread runtime.
    if let Some(arg0) = std::env::args_os().next() {
        if let Some(multicall::MulticallMode::GitRemoteGitback) = multicall::classify_argv0(&arg0) {
            let args: Vec<String> = std::env::args().collect();
            multicall::run_git_remote_gitback(args);
            // multicall::run_git_remote_gitback returns `!` — execution
            // never reaches here.
        }
    }

    // Keep resolved-secret plaintext (the SecretCache + every copy the
    // substitute→apply pipeline makes) off disk and out of a debugger:
    // disable core dumps and mark the process non-dumpable before any
    // secret can be resolved. Best-effort; see platform::process.
    platform::process::harden_process_memory();

    // Regular agent flow: build the multi-thread tokio runtime and
    // hand off to async_main. Equivalent to what `#[tokio::main]`
    // would have done, but explicit so the multicall fast-path above
    // can run outside any runtime.
    tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .context("building tokio runtime")?
        .block_on(async_main())
}

async fn async_main() -> Result<()> {
    // Install the default TLS crypto provider before any TLS operations.
    // install_default returns Err if a provider is already installed —
    // fine in multicall-binary mode (`git-remote-gitback` re-enters
    // async_main from a parent invocation that already installed).
    let _ = rustls::crypto::ring::default_provider().install_default();

    let cli = parse_args()?;
    setup_logger(&cli.log_level);

    // Anchor the agent's uptime clock so the `agent_self` trait can
    // report process uptime independent of host uptime. Called once
    // here, before any collect() can fire.
    vigo::traits::agent_self::init_start_marker();

    // Ensure data directory and swarm directories exist. data_dir is the
    // critical path — fail-fast if it can't be created. The swarm subdirs
    // are sub-feature scaffolding; log on failure and let CM core run.
    std::fs::create_dir_all(&cli.data_dir)
        .with_context(|| format!("create data directory {}", cli.data_dir.display()))?;
    let swarm_base = platform::paths::default_swarm_base();
    for dir in [
        platform::paths::default_blobs_dir(),
        platform::paths::default_seeds_dir(),
        swarm_base.join("files"),
    ] {
        if let Err(e) = std::fs::create_dir_all(&dir) {
            warn!(
                path = %dir.display(),
                error = %e,
                "failed to create swarm directory; swarm features may not work"
            );
        }
    }

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

            save_envoy_id(&cli.data_dir, &resp.envoy_id)?;

            // Persist server address so subsequent runs don't need --server.
            if let Err(e) = config::save(&cli.data_dir, &[("server", &cli.server)]) {
                warn!(error = %e, "failed to save config");
            }

            // Store server's bundle-signing public key for future verification.
            if !resp.server_pubkey_pem.is_empty() {
                save_server_pubkey(&cli.data_dir, &resp.server_pubkey_pem)?;
                info!("stored server bundle-signing public key");
            }

            if !resp.bolt_addr.is_empty() {
                save_bolt_addr(&cli.data_dir, &resp.bolt_addr)?;
                info!(bolt_addr = %resp.bolt_addr, "spanner routed to bolt");
            }

            // Persist fallback servers for agent failover.
            if !resp.fallback_servers.is_empty() {
                let fb = resp.fallback_servers.join(",");
                if let Err(e) = config::save(&cli.data_dir, &[("fallback_servers", &fb)]) {
                    warn!(error = %e, "failed to save fallback_servers to config");
                }
                info!(fallback_servers = %fb, "stored fallback servers for failover");
            }

            info!(envoy_id = %resp.envoy_id, message = %resp.message, "registration successful");
            Ok(())
        }

        Some(Commands::Gitback { args }) => {
            // All vigo swarm gitback subcommands are pure local state reads
            // and/or user-owned `git clone` spawns. Dispatcher lives
            // in agent/src/gitback/cli.rs.
            vigo::swarm::gitback::cli::run(args.clone())
        }

        Some(Commands::Lockbox { args }) => {
            // Lockbox verbs. Week 1 (init/enroll/status) is pure
            // user-home file ops; later weeks add session-helper IPC.
            vigo::swarm::lockbox::cli::run(args.clone())
        }

        Some(Commands::LockboxSessionDaemon { args }) => {
            // Hidden: lockbox session helper daemon entry point.
            // Not in `vigo --help`; invoked only by `vigo swarm lockbox unlock`.
            vigo::swarm::lockbox::session::daemon_main(args.clone()).await
        }

        Some(Commands::SelfFleet { args }) => {
            // Per-user puddle identity (ADR-014). Local-only verbs
            // (init / pubkey / unlock / lock / pair / status) plus
            // `join --from <host>` which talks mTLS over the swarm
            // peer port to retrieve the wrapped blob.
            vigo::swarm::puddle::cli::run(args.clone())
        }

        Some(Commands::SelfFleetSessionDaemon { args }) => {
            // Hidden: puddle session helper daemon entry point.
            // Spawned by `vigo swarm puddle unlock`.
            vigo::swarm::puddle::session::daemon_main(args.clone()).await
        }

        Some(Commands::ServicePuddleDaemon { args }) => {
            // Hidden: service-account puddle helper daemon entry point.
            // Spawned at agent boot by `try_spawn_service_puddle_helper`
            // when `/srv/vigo/puddle/identity.wrapped` exists.
            vigo::swarm::puddle::service_session::daemon_main(args.clone()).await
        }

        Some(Commands::Curator { args }) => {
            // Curator (ADR-024) artifact-registry CLI. Slice 4b ships
            // read-only verbs (status/list/inspect/versions/resolve/
            // pull) — pure local state reads + the existing Resolver
            // for resolve/pull (server-mediated with a cached-catalog
            // fallback). Mutating verbs land in slice 4c.
            vigo::swarm::curator::cli::run(args.clone())
        }

        Some(Commands::Poolq { args }) => {
            // `vigo swarm poolq <verb>` — ADR-029 founder + consumer CLI.
            // Read verbs (topics / subscribe / read / tail / seek / offset
            // / status) hit the public REST endpoints with the agent's
            // bootstrap TLS; `post` signs via the per-user puddle session
            // helper and writes under ~/.vigo-poolq/. No helper daemon —
            // see ADR-029 "No root helper daemon".
            vigo::swarm::poolq::cli::run(args.clone())
        }

        Some(Commands::SwarmApplyCleanup) => {
            // Hidden: server-dispatched swarm-cache hygiene sweep.
            // Returns a one-line operator summary on stdout so the
            // dispatching side has audit-friendly output.
            vigo::swarm::apply_cleanup()
        }

        Some(Commands::SwarmDiag { args }) => {
            // Read-only envoy-wide diagnostic verbs (0.41.1+):
            // status / seeds {list,show} / peers list. Reads the
            // mode-0644 daemon-canonical mirrors under
            // /var/lib/vigo/swarm/. No sudo required.
            vigo::swarm::cli::run(args.clone())
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

            // Save TLS artifacts and envoy UUID.
            auth::keypair::save_tls_key(&cli.data_dir, &tls_key)?;
            auth::keypair::save_tls_cert(&cli.data_dir, &resp.signed_cert_pem)?;
            auth::keypair::save_ca_cert(&cli.data_dir, &resp.ca_cert_pem)?;
            save_envoy_id(&cli.data_dir, &resp.envoy_id)?;

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
                envoy_id = %resp.envoy_id,
                tls_cert = %auth::keypair::tls_cert_path(&cli.data_dir).display(),
                ca_cert = %auth::keypair::ca_cert_path(&cli.data_dir).display(),
                "bootstrap successful"
            );

            // Surface license capacity warnings from the server. Printed
            // to stderr so the curl one-liner shows the notice without
            // polluting machine-readable stdout, and so it lands in the
            // journal when invoked via bootstrap scripts.
            if !resp.license_warning.is_empty() {
                eprintln!("\nWARNING: {}\n", resp.license_warning);
                if resp.license_max > 0 {
                    eprintln!(
                        "  Active envoys: {} / {} (remaining: {})\n",
                        resp.envoys_active, resp.license_max, resp.envoys_remaining
                    );
                }
            }

            // Install system service if requested.
            if *install_service {
                let install_result = install_platform_service(
                    bootstrap_server,
                    grpc_port,
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
                            &resp.envoy_id,
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
                            "Service install failed: {}\nStart the agent manually:\n  vigo --server <grpc-host>:1530",
                            e
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

            // Force-exit: the gRPC channel's background HTTP/2 task can
            // linger after run_once returns, preventing tokio from shutting
            // down. Since all work is done, exit immediately.
            let code = if result.is_ok() { 0 } else { 1 };
            if let Err(ref e) = result {
                eprintln!("Error: {e:#}");
            }
            std::process::exit(code);
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
