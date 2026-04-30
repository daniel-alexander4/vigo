// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! `vigo swarm self-fleet *` verb tree (ADR-014).

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use anyhow::{anyhow, bail, Context, Result};
use ed25519_dalek::SigningKey;

use super::config;
use super::pair;
use super::session;
use crate::swarm::cryptsync::crypt::{
    encode_sign_pubkey, generate_signing_keypair, unwrap_signing_key, wrap_signing_key,
    SIGN_PUBKEY_LEN,
};

const DEFAULT_IDLE_MINUTES: u32 = 60;

pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "help" | "--help" | "-h" => {
            print_usage();
            Ok(())
        }
        "init" => cmd_init(&args[1..]),
        "pubkey" => cmd_pubkey(&args[1..]),
        "unlock" => cmd_unlock(&args[1..]),
        "lock" => cmd_lock(&args[1..]),
        "pair" => cmd_pair(&args[1..]),
        "join" => cmd_join(&args[1..]),
        "members" => cmd_members(&args[1..]),
        "rotate-passphrase" => cmd_rotate_passphrase(&args[1..]),
        "status" => cmd_status(&args[1..]),
        other => bail!(
            "unknown `vigo swarm self-fleet` subcommand: {}\nrun `vigo swarm self-fleet --help` for usage",
            other
        ),
    }
}

fn print_usage() {
    eprintln!(
        "vigo swarm self-fleet — per-user fleet identity (ADR-014)

USAGE:
    vigo swarm self-fleet <SUBCOMMAND>

LIFECYCLE:
    init                       Generate a new fleet keypair on the founder envoy.
    pair                       Issue a 5-minute pairing code so a peer envoy can join.
    join --from <host>         Receive the wrapped key from <host> using the pairing code.

DAILY:
    unlock                     Unwrap the key and start the session helper.
    lock                       Scrub the held key and stop the session helper.

INFO / MAINTENANCE:
    pubkey                     Print the local fleet pubkey fingerprint.
    members                    Show fleet identity state on this envoy.
    rotate-passphrase          Re-wrap the key under a new passphrase (this envoy only).
    status                     Show local self-fleet state.

State lives at ~/.vigo-fleet/. No root required. Does not contact the Vigo server."
    );
}

// ---------- init ----------

fn cmd_init(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if super::is_initialized(&home) {
        bail!(
            "self-fleet already initialized at {} — to re-init, delete the directory first \
             (this orphans every other envoy in the fleet; usually you want `pair` instead).",
            config::dot_dir(&home).display()
        );
    }
    let dot = config::dot_dir(&home);
    create_user_dir(&dot, &username, 0o700)
        .with_context(|| format!("creating {}", dot.display()))?;

    let password = prompt_password(true)?;
    if password.is_empty() {
        bail!("passphrase must not be empty");
    }

    let keypair = generate_signing_keypair();
    let pubkey_text = encode_sign_pubkey(&keypair.verifying_key());
    let salt = crate::swarm::cryptsync::crypt::fresh_salt();
    let blob = wrap_signing_key(&keypair, password.as_bytes(), &salt)
        .context("wrapping fleet identity")?;

    write_private(&config::wrapped_path(&home), &blob, &username)?;
    write_private(&config::salt_path(&home), &salt, &username)?;
    write_pubkey(&config::pubkey_path(&home), &pubkey_text, &username)?;

    println!("wrote {} (0600, wrapped)", config::wrapped_path(&home).display());
    println!("wrote {} (0600)", config::salt_path(&home).display());
    println!("wrote {} (0644)", config::pubkey_path(&home).display());
    println!();
    println!("fleet pubkey: {}", pubkey_text);
    println!();
    println!("This envoy is now the founder of your self-fleet. To add more envoys:");
    println!("  1) on this envoy:        `vigo swarm self-fleet pair`");
    println!("  2) on the joining envoy: `vigo swarm self-fleet join --from {}`",
        local_hostname_or("<this-host>"));
    Ok(())
}

// ---------- pubkey ----------

fn cmd_pubkey(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    match super::local_fleet_pubkey(&home)? {
        Some(pk) => {
            println!("{}", pk);
            Ok(())
        }
        None => bail!("self-fleet not initialized — run `vigo swarm self-fleet init`"),
    }
}

// ---------- unlock ----------

fn cmd_unlock(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!(
            "self-fleet not initialized on this envoy — run `vigo swarm self-fleet init` \
             (founder) or `vigo swarm self-fleet join --from <host>` (peer envoy) first"
        );
    }
    let blob = fs::read(config::wrapped_path(&home))
        .with_context(|| format!("reading {}", config::wrapped_path(&home).display()))?;
    let salt = fs::read(config::salt_path(&home))
        .with_context(|| format!("reading {}", config::salt_path(&home).display()))?;

    let password = prompt_password(false)?;
    if password.is_empty() {
        bail!("passphrase must not be empty");
    }
    let key = unwrap_signing_key(&blob, password.as_bytes(), &salt)?;

    let parent_pid = resolve_shell_pid()
        .unwrap_or_else(|| unsafe { libc::getppid() } as u32);
    session::spawn_helper(&key, parent_pid, DEFAULT_IDLE_MINUTES, &home)?;

    println!(
        "✓ Unlocked. Self-fleet session helper running until logout, lock, or {} min idle.",
        DEFAULT_IDLE_MINUTES
    );
    Ok(())
}

// ---------- lock ----------

fn cmd_lock(_args: &[String]) -> Result<()> {
    session::request_lock()?;
    println!("✓ Locked. Fleet key scrubbed.");
    Ok(())
}

// ---------- pair ----------

fn cmd_pair(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!("self-fleet not initialized on this envoy — `init` first");
    }

    // Reissue: a previous pair sentinel may exist with a different
    // code or expired. Just overwrite — last writer wins, single
    // pending session per envoy.
    let code = pair::generate_pair_code();
    pair::write_pending(&home, &code, &username)?;

    println!();
    println!("Pairing code:  {}", code);
    println!("Valid for:     5 minutes ({} seconds)", pair::PAIR_TTL_SECS);
    println!();
    println!("On the joining envoy, run:");
    println!("    vigo swarm self-fleet join --from {}", local_hostname_or("<this-host>"));
    println!("then enter this code when prompted, then enter your fleet passphrase.");
    println!();
    println!("The code is single-use; re-run `pair` to issue another.");
    Ok(())
}

// ---------- join ----------

fn cmd_join(args: &[String]) -> Result<()> {
    let mut from: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--from" => {
                i += 1;
                from = Some(args.get(i).context("--from requires a hostname")?.clone());
            }
            s if s.starts_with("--") => bail!("unknown flag: {}", s),
            s => bail!("unexpected positional argument: {}", s),
        }
        i += 1;
    }
    let from = from.context("--from <host> is required")?;
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;

    if super::is_initialized(&home) {
        bail!(
            "self-fleet is already initialized at {} — `join` would overwrite local state. \
             To re-pair, delete the directory first.",
            config::dot_dir(&home).display()
        );
    }

    println!("Pairing with {} for user {}.", from, username);
    let code = prompt_line("pairing code (NNNN-NNNN): ")?;
    if code.trim().is_empty() {
        bail!("pairing code must not be empty");
    }

    // Build the mTLS HTTP client. The agent's cert + key live under
    // the standard data dir (e.g. /etc/vigo-envoy/certs/). The key
    // file is 0600 root-owned, so this verb requires root — tell
    // the user to retry via sudo if we can't read it.
    let tls = load_envoy_tls().context(
        "self-fleet join needs the agent's mTLS key (0600 root-owned). \
         Run via sudo: `sudo vigo swarm self-fleet join --from <host>`",
    )?;
    let agent = build_mtls_agent(&tls)?;
    let url = format!(
        "https://{}:{}{}{}",
        from,
        crate::swarm::server::DEFAULT_PORT,
        pair::PAIR_CLAIM_PREFIX,
        username
    );
    let body = serde_json::json!({ "code": code.trim() });
    let body = serde_json::to_vec(&body).context("serializing claim body")?;

    let resp = agent
        .post(&url)
        .header("Content-Type", "application/json")
        .send(body)
        .with_context(|| format!("POST {}", url))?;
    let status = resp.status();
    if !status.is_success() {
        let code = status.as_u16();
        let mut resp = resp;
        let msg = resp.body_mut().read_to_string().unwrap_or_default();
        bail!("pair-claim failed (HTTP {}): {}", code, msg.trim());
    }
    let mut resp = resp;
    let bytes = resp.body_mut().read_to_vec().context("reading response body")?;
    let (salt, wrapped) = pair::parse_pair_response(&bytes)?;

    // Local passphrase prompt — never crosses the wire. We unwrap
    // here purely to verify the wrapped blob can be opened with the
    // user's passphrase (catches a corrupted-in-transit blob and
    // wrong-passphrase early). The unwrapped key is dropped on
    // function return; the user runs `unlock` afterwards to start a
    // session in their own (non-sudo) context.
    let passphrase = prompt_password(false)?;
    if passphrase.is_empty() {
        bail!("passphrase must not be empty");
    }
    let key = unwrap_signing_key(&wrapped, passphrase.as_bytes(), &salt)
        .context("unwrap with provided passphrase")?;

    // Persist locally as if this envoy had run `init`, but with the
    // wire-received key + salt + derived pubkey. The wrapped blob on
    // disk is identical bytes to what the founder holds.
    let dot = config::dot_dir(&home);
    create_user_dir(&dot, &username, 0o700)?;
    let pubkey_text = encode_sign_pubkey(&key.verifying_key());
    write_private(&config::wrapped_path(&home), &wrapped, &username)?;
    write_private(&config::salt_path(&home), &salt, &username)?;
    write_pubkey(&config::pubkey_path(&home), &pubkey_text, &username)?;

    println!();
    println!("✓ Joined fleet {}.", &pubkey_text);
    println!(
        "  wrote {} (0600, wrapped)",
        config::wrapped_path(&home).display()
    );
    println!(
        "  wrote {} (0600)",
        config::salt_path(&home).display()
    );
    println!(
        "  wrote {} (0644)",
        config::pubkey_path(&home).display()
    );
    println!();
    println!(
        "Run `vigo swarm self-fleet unlock` (no sudo) to activate the session on this envoy."
    );
    Ok(())
}

// ---------- members ----------

fn cmd_members(_args: &[String]) -> Result<()> {
    // ADR-014 frames `members` as "list fleet pubkeys advertised by
    // peers in the swarm gossip (informational; not authoritative)."
    // Phase 1 surfaces only the local view: this envoy's pubkey +
    // initialization status. Cross-envoy discovery rides ADR-015 /
    // ADR-016, which extend swarm announcements to carry fleet
    // pubkeys; until then `members` is honest about scope.
    let home = user_home_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!("self-fleet not initialized on this envoy");
    }
    let pubkey = super::local_fleet_pubkey(&home)?
        .ok_or_else(|| anyhow!("identity.pub missing despite is_initialized"))?;
    let host = local_hostname_or("<this-host>");
    println!("self-fleet members (this envoy's view):");
    println!("  {} (this envoy)  {}", host, pubkey);
    println!();
    println!(
        "Cross-envoy discovery via swarm gossip lands with ADR-015 / ADR-016. Until then \
         `members` only shows the local envoy."
    );
    Ok(())
}

// ---------- rotate-passphrase ----------

fn cmd_rotate_passphrase(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!("self-fleet not initialized on this envoy");
    }
    let blob = fs::read(config::wrapped_path(&home))?;
    let salt = fs::read(config::salt_path(&home))?;

    println!("Re-wrapping the local fleet key under a new passphrase.");
    println!("Other envoys retain the old passphrase until rotated separately.");
    println!();
    let old = prompt_password_labeled("current passphrase: ")?;
    let key = unwrap_signing_key(&blob, old.as_bytes(), &salt)
        .context("unwrap with current passphrase")?;
    let new = prompt_password_labeled("new passphrase: ")?;
    let confirm = prompt_password_labeled("confirm new passphrase: ")?;
    if new != confirm {
        bail!("passphrases do not match");
    }
    if new.is_empty() {
        bail!("passphrase must not be empty");
    }

    let new_salt = crate::swarm::cryptsync::crypt::fresh_salt();
    let new_blob = wrap_signing_key(&key, new.as_bytes(), &new_salt)?;

    // Atomic write via tempfile + rename so a crash mid-rotate
    // doesn't leave a half-written wrapped blob in place.
    let wrapped_path = config::wrapped_path(&home);
    let salt_path = config::salt_path(&home);
    let wrapped_tmp = wrapped_path.with_extension("wrapped.tmp");
    let salt_tmp = salt_path.with_extension("salt.tmp");
    write_private(&wrapped_tmp, &new_blob, &username)?;
    write_private(&salt_tmp, &new_salt, &username)?;
    fs::rename(&wrapped_tmp, &wrapped_path)
        .with_context(|| format!("rename → {}", wrapped_path.display()))?;
    fs::rename(&salt_tmp, &salt_path)
        .with_context(|| format!("rename → {}", salt_path.display()))?;

    println!();
    println!("✓ Local fleet key re-wrapped. Pubkey unchanged: {}",
        encode_sign_pubkey(&key.verifying_key()));
    println!("  Run `rotate-passphrase` on each other fleet envoy to rotate them too.");
    Ok(())
}

// ---------- status ----------

fn cmd_status(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let dot = config::dot_dir(&home);
    println!("self-fleet status");
    println!("  home:             {}", home.display());
    println!("  dot_dir:          {}", dot.display());
    println!("  initialized:      {}", super::is_initialized(&home));
    if super::is_initialized(&home) {
        match super::local_fleet_pubkey(&home)? {
            Some(pk) => println!("  fleet pubkey:     {}", pk),
            None => println!("  fleet pubkey:     (identity.pub missing)"),
        }
    }
    let pair_pending = config::pair_pending_path(&home);
    if pair_pending.exists() {
        match pair::read_pending(&home) {
            Ok(Some(spec)) => {
                let now = now_secs();
                let remaining = (spec.expires_at - now).max(0);
                println!(
                    "  pair pending:     yes (expires in {} s)",
                    remaining
                );
            }
            _ => println!("  pair pending:     yes (sentinel unparseable)"),
        }
    } else {
        println!("  pair pending:     no");
    }
    match session::request_status() {
        Ok(msg) => println!("  session helper:   {}", msg),
        Err(_) => println!("  session helper:   not running (locked)"),
    }
    Ok(())
}

// ---------- helpers ----------

fn user_for_invocation() -> Result<String> {
    #[cfg(unix)]
    {
        let uid = unsafe { libc::getuid() };
        if uid != 0 {
            if let Some(name) = passwd_name_for_uid(uid) {
                return Ok(name);
            }
        }
    }
    if let Ok(s) = std::env::var("SUDO_USER") {
        if !s.is_empty() && s != "root" {
            return Ok(s);
        }
    }
    if let Ok(s) = std::env::var("USER") {
        if !s.is_empty() {
            return Ok(s);
        }
    }
    bail!(
        "could not resolve invoking user (set $SUDO_USER or $USER, or run with non-root real uid)"
    )
}

fn user_home_for_invocation() -> Result<PathBuf> {
    let username = user_for_invocation()?;
    crate::swarm::cryptsync::home_for_user_pub(&username)
        .ok_or_else(|| anyhow!("could not resolve home directory for user {}", username))
}

#[cfg(unix)]
fn passwd_name_for_uid(uid: u32) -> Option<String> {
    let passwd = fs::read_to_string("/etc/passwd").ok()?;
    for line in passwd.lines() {
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() >= 7 {
            if let Ok(u) = fields[2].parse::<u32>() {
                if u == uid {
                    return Some(fields[0].to_string());
                }
            }
        }
    }
    None
}

#[cfg(target_os = "linux")]
fn resolve_shell_pid() -> Option<u32> {
    let me_name = std::env::current_exe()
        .ok()
        .and_then(|p| p.file_name().map(|n| n.to_string_lossy().to_string()))
        .unwrap_or_default();
    let intermediates: &[&str] = &["sudo", "env", "su", "doas"];
    let mut pid = unsafe { libc::getppid() } as u32;
    for _ in 0..20 {
        if pid <= 1 {
            return None;
        }
        let comm = fs::read_to_string(format!("/proc/{}/comm", pid))
            .ok()?
            .trim()
            .to_string();
        let is_intermediate =
            intermediates.iter().any(|s| **s == comm) || comm == me_name;
        if !is_intermediate {
            return Some(pid);
        }
        let status = fs::read_to_string(format!("/proc/{}/status", pid)).ok()?;
        let mut next: Option<u32> = None;
        for line in status.lines() {
            if let Some(rest) = line.strip_prefix("PPid:") {
                next = rest.trim().parse().ok();
                break;
            }
        }
        pid = next?;
    }
    None
}

#[cfg(not(target_os = "linux"))]
fn resolve_shell_pid() -> Option<u32> {
    None
}

fn prompt_password(confirm: bool) -> Result<String> {
    let pw = read_one_password("self-fleet passphrase: ")?;
    if confirm {
        let pw2 = read_one_password("confirm passphrase: ")?;
        if pw != pw2 {
            bail!("passphrases do not match");
        }
    }
    Ok(pw)
}

fn prompt_password_labeled(prompt: &str) -> Result<String> {
    read_one_password(prompt)
}

fn read_one_password(prompt: &str) -> Result<String> {
    #[cfg(unix)]
    {
        use std::io::BufRead;
        use std::os::unix::io::AsRawFd;
        let stdin_is_tty = unsafe { libc::isatty(std::io::stdin().as_raw_fd()) } != 0;
        if stdin_is_tty {
            return rpassword::prompt_password(prompt).context("reading passphrase");
        }
        let stdin = std::io::stdin();
        let mut line = String::new();
        stdin
            .lock()
            .read_line(&mut line)
            .context("reading passphrase from stdin")?;
        Ok(line.trim_end_matches(['\r', '\n']).to_string())
    }
    #[cfg(not(unix))]
    {
        rpassword::prompt_password(prompt).context("reading passphrase")
    }
}

fn prompt_line(prompt: &str) -> Result<String> {
    use std::io::BufRead;
    print!("{}", prompt);
    std::io::stdout().flush().ok();
    let stdin = std::io::stdin();
    let mut line = String::new();
    stdin.lock().read_line(&mut line).context("reading line")?;
    Ok(line.trim().to_string())
}

#[cfg(unix)]
fn write_private(path: &Path, content: &[u8], username: &str) -> Result<()> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)
        .with_context(|| format!("creating {}", path.display()))?;
    f.write_all(content)?;
    f.sync_all().ok();
    crate::swarm::cryptsync::chown_to_user_pub(path, username)
        .with_context(|| format!("chown {} to {}", path.display(), username))?;
    Ok(())
}

#[cfg(not(unix))]
fn write_private(path: &Path, content: &[u8], _username: &str) -> Result<()> {
    fs::write(path, content).with_context(|| format!("writing {}", path.display()))
}

#[cfg(unix)]
fn write_pubkey(path: &Path, pubkey: &str, username: &str) -> Result<()> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o644)
        .open(path)
        .with_context(|| format!("creating {}", path.display()))?;
    writeln!(f, "{}", pubkey)?;
    f.sync_all().ok();
    crate::swarm::cryptsync::chown_to_user_pub(path, username)
        .with_context(|| format!("chown {} to {}", path.display(), username))?;
    Ok(())
}

#[cfg(not(unix))]
fn write_pubkey(path: &Path, pubkey: &str, _username: &str) -> Result<()> {
    fs::write(path, format!("{}\n", pubkey))
        .with_context(|| format!("writing {}", path.display()))
}

#[cfg(unix)]
fn create_user_dir(path: &Path, username: &str, mode: u32) -> Result<()> {
    use std::os::unix::fs::DirBuilderExt;
    if !path.is_dir() {
        std::fs::DirBuilder::new()
            .recursive(true)
            .mode(mode)
            .create(path)
            .with_context(|| format!("creating {}", path.display()))?;
    }
    crate::swarm::cryptsync::chown_to_user_pub(path, username)
        .with_context(|| format!("chown {} to {}", path.display(), username))?;
    Ok(())
}

#[cfg(not(unix))]
fn create_user_dir(path: &Path, _username: &str, _mode: u32) -> Result<()> {
    if path.is_dir() {
        return Ok(());
    }
    fs::create_dir_all(path)
        .with_context(|| format!("creating {}", path.display()))
}

fn local_hostname_or(default: &str) -> String {
    fs::read_to_string("/etc/hostname")
        .ok()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| default.to_string())
}

fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

/// Load the agent's mTLS certs from the standard data dir
/// (`/etc/vigo-envoy/` on managed nodes; resolves via
/// `crate::platform::paths::default_data_dir()`). Both the cert and
/// key files must exist; if either is missing or unreadable the
/// caller should surface the "run via sudo" hint, since the key file
/// is 0600 root-owned.
fn load_envoy_tls() -> Result<crate::update::TlsCerts> {
    let dir = crate::platform::paths::default_data_dir();
    if !crate::auth::keypair::has_bootstrap_certs(&dir) {
        bail!(
            "no mTLS certs at {} — has this envoy been bootstrapped?",
            dir.display()
        );
    }
    let key_path = crate::auth::keypair::tls_key_path(&dir);
    // Probe-readable check: if we can't open the key file, we
    // definitely can't talk mTLS. ureq would surface the same
    // failure later but with a less specific message.
    if let Err(e) = fs::File::open(&key_path) {
        bail!(
            "cannot open {} ({}). Run via sudo to access the agent's mTLS key.",
            key_path.display(),
            e
        );
    }
    Ok(crate::update::TlsCerts {
        ca_cert: crate::auth::keypair::ca_cert_path(&dir)
            .to_string_lossy()
            .to_string(),
        client_cert: crate::auth::keypair::tls_cert_path(&dir)
            .to_string_lossy()
            .to_string(),
        client_key: key_path.to_string_lossy().to_string(),
    })
}

fn build_mtls_agent(tls: &crate::update::TlsCerts) -> Result<ureq::Agent> {
    crate::swarm::cryptsync::pusher::build_agent_for_mod(tls)
}

// Defensive — silence dead-code warnings while the feature lands.
#[allow(dead_code)]
fn _types_check_marker(_k: &SigningKey) {
    let _ = SIGN_PUBKEY_LEN;
}
