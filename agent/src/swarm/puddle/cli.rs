// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! `vigo swarm puddle *` verb tree (ADR-014).

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use anyhow::{anyhow, bail, Context, Result};
use ed25519_dalek::SigningKey;

use super::codes;
use super::config;
use super::name;
use super::names_cache;
use super::pair;
use super::rekey;
use super::session;
use crate::swarm::lockbox::crypt::{
    encode_sign_pubkey, generate_signing_keypair, unwrap_signing_key, wrap_signing_key,
    SIGN_PUBKEY_LEN,
};

pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "help" | "--help" | "-h" => {
            print_usage();
            Ok(())
        }
        "init" => with_puddle_authz(|| cmd_init(&args[1..])),
        "pubkey" => with_puddle_authz(|| cmd_pubkey(&args[1..])),
        "unlock" => with_puddle_authz(|| cmd_unlock(&args[1..])),
        "lock" => with_puddle_authz(|| cmd_lock(&args[1..])),
        "pair" => with_puddle_authz(|| cmd_pair(&args[1..])),
        "join" => with_puddle_authz(|| cmd_join(&args[1..])),
        "leave" => with_puddle_authz(|| cmd_leave(&args[1..])),
        "rekey-pair" => with_puddle_authz(|| cmd_rekey_pair(&args[1..])),
        "rekey" => with_puddle_authz(|| cmd_rekey(&args[1..])),
        "members" => with_puddle_authz(|| cmd_members(&args[1..])),
        "rotate-passphrase" => with_puddle_authz(|| cmd_rotate_passphrase(&args[1..])),
        "status" => with_puddle_authz(|| cmd_status(&args[1..])),
        "name" => with_puddle_authz(|| cmd_name(&args[1..])),
        other => bail!(
            "unknown `vigo swarm puddle` subcommand: {}\nrun `vigo swarm puddle --help` for usage",
            other
        ),
    }
}

/// Refuse early if the invoking user lacks `puddle: true` in their
/// usercrate (0.41.0+). The gate consults the daemon-canonical cache
/// at `/var/lib/vigo/usercrate-policy/<user>.json` written by the
/// user executor on each reconcile pass — no server round-trip
/// required.
fn with_puddle_authz<F, T>(f: F) -> Result<T>
where
    F: FnOnce() -> Result<T>,
{
    let user = user_for_invocation()?;
    crate::swarm::policy::require_puddle(&user)?;
    f()
}

fn print_usage() {
    eprintln!(
        "vigo swarm puddle — per-user puddle identity (ADR-014)

USAGE:
    vigo swarm puddle <SUBCOMMAND>

LIFECYCLE:
    init                       Generate a new puddle keypair on the founder envoy.
    pair                       Issue a 5-minute pairing code so a peer envoy can join.
    join --from <host>         Receive the wrapped key from <host> using the pairing code.
    leave [--yes]              Leave the puddle on this envoy: sign LEAVE for lockbox
                               peers, scrub ~/.vigo-puddle/, ~/.lockbox/, ~/lockbox/,
                               and ~/.vigo-gitback/.

REKEY (ADR-018, puddle keypair rotation):
    rekey-pair                 Rotate the puddle keypair and issue a 5-minute claim code.
    rekey-pair --close         Close the active 24h rekey window early.
    rekey --from <host>        Migrate this envoy to the founder's new puddle keypair.

DAILY:
    unlock [--idle-mins <N>]   Unwrap the key and start the session helper. The helper
                               re-locks after <N> minutes idle (default 24h), on logout,
                               or on `lock`. The idle timer resets on each use.
    lock                       Scrub the held key and stop the session helper.

FRIENDLY NAMES (ADR-022):
    name set <name>            Claim <name> for this puddle (puddle must be unlocked).
                               Makes `gitback://<name>/<repo>` resolve to your projects,
                               alongside the hex `gitback://<project_id>/<repo>` form.
    name clear [--yes]         Drop this puddle's name claim. Breaks every
                               `gitback://<name>/…` URL anyone wrote down; the hex
                               project_id URLs keep working.
    name show                  Show this puddle's name claim and the fleet view of it.

INFO / MAINTENANCE:
    pubkey                     Print the local puddle pubkey fingerprint.
    members                    Show puddle identity state on this envoy.
    rotate-passphrase          Re-wrap the key under a new passphrase (this envoy only).
    status                     Show local puddle state.

State lives at ~/.vigo-puddle/. No root required. Does not contact the Vigo server."
    );
}

// ---------- init ----------

fn cmd_init(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if super::is_initialized(&home) {
        bail!(
            "puddle already initialized at {} — to re-init, delete the directory first \
             (this orphans every other envoy in the puddle; usually you want `pair` instead).",
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
    let salt = crate::swarm::lockbox::crypt::fresh_salt();
    let blob = wrap_signing_key(&keypair, password.as_bytes(), &salt)
        .context("wrapping puddle identity")?;

    write_private(&config::wrapped_path(&home), &blob, &username)?;
    write_private(&config::salt_path(&home), &salt, &username)?;
    write_pubkey(&config::pubkey_path(&home), &pubkey_text, &username)?;

    println!("wrote {} (0600, wrapped)", config::wrapped_path(&home).display());
    println!("wrote {} (0600)", config::salt_path(&home).display());
    println!("wrote {} (0644)", config::pubkey_path(&home).display());
    println!();
    println!("puddle pubkey: {}", pubkey_text);
    println!();
    println!("This envoy is now the founder of your puddle. To add more envoys:");
    println!("  1) on this envoy:        `vigo swarm puddle pair`");
    println!("  2) on the joining envoy: `vigo swarm puddle join --from {}`",
        local_hostname_or("<this-host>"));
    Ok(())
}

// ---------- pubkey ----------

fn cmd_pubkey(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    match super::local_puddle_pubkey(&home)? {
        Some(pk) => {
            println!("{}", pk);
            Ok(())
        }
        None => bail!("puddle not initialized — run `vigo swarm puddle init`"),
    }
}

// ---------- unlock ----------

/// Humanize an idle-timeout for the unlock confirmation: whole hours
/// as `Nh`, otherwise `Nm`.
fn fmt_idle(mins: u32) -> String {
    if mins % 60 == 0 {
        format!("{}h", mins / 60)
    } else {
        format!("{}m", mins)
    }
}

fn cmd_unlock(args: &[String]) -> Result<()> {
    let mut idle_mins = session::DEFAULT_IDLE_MINUTES;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--idle-mins" => {
                i += 1;
                let v: u32 = args
                    .get(i)
                    .context("--idle-mins requires a value in minutes")?
                    .parse()
                    .context("--idle-mins must be a positive integer (minutes)")?;
                if v < 1 {
                    bail!("--idle-mins must be ≥ 1");
                }
                idle_mins = v;
            }
            s if s.starts_with("--") => bail!("unknown flag: {}", s),
            s => bail!("unexpected positional argument: {}", s),
        }
        i += 1;
    }

    let home = user_home_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!(
            "puddle not initialized on this envoy — run `vigo swarm puddle init` \
             (founder) or `vigo swarm puddle join --from <host>` (peer envoy) first"
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
    session::spawn_helper(&key, parent_pid, idle_mins, &home)?;

    println!(
        "✓ Unlocked. Puddle session helper running until logout, lock, or {} idle.",
        fmt_idle(idle_mins)
    );

    // ADR-016 slice 4: now that the puddle session helper is
    // running, this is the one moment at which a fresh envoy can
    // bootstrap lockbox without the user having to type a second
    // passphrase — puddle-mode wrap-key derivation goes through the
    // helper we just spawned. Two paths:
    //
    //   - Identity missing → run `lockbox puddle init` to create the
    //     per-envoy age key, recipients, and puddle-membership cert.
    //   - Identity present + wrap_mode=Puddle → run `lockbox unlock`
    //     to start the lockbox session helper. Without this step,
    //     the auto-encrypt watcher queues plaintext drops indefinitely
    //     because it can't derive the wrap key without the lockbox
    //     helper running, even though the puddle helper has the
    //     material. Operator hit this on every `puddle unlock` after
    //     the first one until 0.39.6.
    //   - Identity present + wrap_mode=Standalone → skip. Standalone
    //     mode expects an interactive passphrase prompt that the
    //     puddle-unlock flow shouldn't surprise the user with.
    //
    // Failures don't undo the unlock — we surface the error and keep
    // going so the operator can re-run manually.
    let cs_dot = home.join(".lockbox");
    let cs_identity = cs_dot.join("identity.age");
    let mut bootstrap_ok = true;
    if !cs_identity.exists() {
        println!();
        println!("Bootstrapping lockbox (puddle-mode wrap, no extra passphrase)...");
        match crate::swarm::lockbox::cli::run(vec![
            "puddle".to_string(),
            "init".to_string(),
        ]) {
            Ok(()) => {}
            Err(e) => {
                eprintln!("warning: lockbox auto-init failed: {:#}", e);
                eprintln!(
                    "         re-run manually: `vigo swarm lockbox puddle init`"
                );
                bootstrap_ok = false;
            }
        }
    }
    // Whether lockbox was already enrolled or we just bootstrapped
    // it, start its session helper so encrypt / decrypt / list verbs
    // work immediately. Pre-0.41.2 only auto-started in the
    // already-enrolled branch, leaving freshly-bootstrapped envoys
    // half-ready (operator could encrypt but not decrypt until they
    // ran `lockbox unlock` manually). Skip when bootstrap failed —
    // there's no identity to unlock and the operator needs to fix
    // the bootstrap first.
    if bootstrap_ok {
        println!();
        println!("Starting lockbox session helper (puddle-derived wrap, no extra passphrase)...");
        match crate::swarm::lockbox::cli::run(vec!["unlock".to_string()]) {
            Ok(()) => {}
            Err(e) => {
                eprintln!("warning: lockbox unlock failed: {:#}", e);
                eprintln!(
                    "         re-run manually: `vigo swarm lockbox unlock`"
                );
            }
        }
    }

    // ADR-023: with the session helper up (the one moment with a
    // usable signing key), auto-adopt the gitback projects this
    // puddle founded so `dr_scope: swarm-wide` repos actually reach
    // this envoy. Off the hot path; failures warn only — never undo
    // the unlock.
    let my_pubkey = key.verifying_key().to_bytes();
    adopt_founded_gitback_projects(&home, &my_pubkey);

    Ok(())
}

/// ADR-023: auto-adopt the gitback projects this puddle founded.
///
/// For each project the swarm is currently announcing (cached at
/// `gitback-announced.json` by the agent's catch-up reactor) whose
/// `project_id == sha256(my_puddle_pubkey ‖ handle)` and for which
/// this user has no local state yet, mint the founder-bootstrap state
/// — the same thing `vigo swarm gitback project init` does. The
/// 0.46.0 catch-up reactor then pulls the bare repo within a couple of
/// ticks. Founder-key-current only: a post-rekey (ADR-019) founded
/// project stays anchored to the *genesis* pubkey, so it won't match
/// here — use `gitback project rejoin` for those. Non-founder
/// memberships are never adopted (they need an invitation). Failures
/// warn but never undo the unlock.
fn adopt_founded_gitback_projects(home: &Path, my_pubkey: &[u8; 32]) {
    use crate::swarm::anchor::tokens;
    use crate::swarm::gitback::{cli as gitback_cli, project_peers, project_state};

    let announced = project_peers::load_announced_cache();
    if announced.is_empty() {
        return;
    }
    for ap in announced {
        let pid = tokens::project_id_for(my_pubkey, &ap.handle);
        if hex::encode(pid) != ap.project_id {
            continue; // founded by a different puddle (or a corrupt entry)
        }
        // Already have local state? Steady state after the first
        // unlock — nothing to do, and no warning.
        match project_state::ProjectConfig::load(home, &pid) {
            Ok(Some(_)) => continue,
            Ok(None) => {}
            Err(_) => continue, // transient I/O — retry next unlock
        }
        let scope = ap
            .dr_scope
            .parse()
            .unwrap_or(project_state::DrScope::SwarmWide);
        match gitback_cli::mint_founder_project(home, &ap.handle, scope) {
            Ok(_) => println!(
                "Adopted gitback project '{}' — the catch-up reactor will pull it shortly.",
                ap.handle
            ),
            Err(e) => eprintln!(
                "warning: could not adopt gitback project '{}': {:#}",
                ap.handle, e
            ),
        }
    }
}

// ---------- lock ----------

fn cmd_lock(_args: &[String]) -> Result<()> {
    session::request_lock()?;
    println!("✓ Locked. Puddle key scrubbed.");
    Ok(())
}

// ---------- pair ----------

fn cmd_pair(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!("puddle not initialized on this envoy — `init` first");
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
    println!("    vigo swarm puddle join --from {}", local_hostname_or("<this-host>"));
    println!("then enter this code when prompted, then enter your puddle passphrase.");
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
            "puddle is already initialized at {} — `join` would overwrite local state. \
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
        "puddle join needs the agent's mTLS key (0600 root-owned). \
         Run via sudo: `sudo vigo swarm puddle join --from <host>`",
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
    println!("✓ Joined puddle {}.", &pubkey_text);
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
        "Run `vigo swarm puddle unlock` (no sudo) to activate the session on this envoy."
    );

    // ADR-016 slice 4: the next `puddle unlock` will auto-bootstrap
    // lockbox on this envoy if its identity.age is still missing.
    // The session helper isn't running yet (`join` only persists the
    // wrapped blob; the user activates it on their own login), so we
    // don't run lockbox init inline here — surface the side effect
    // the operator should expect from the unlock that follows.
    let cs_identity = home.join(".lockbox").join("identity.age");
    if !cs_identity.exists() {
        println!();
        println!(
            "note: the next `puddle unlock` on this envoy will also bootstrap lockbox \
             (encrypted P2P sync), wrapped under your puddle identity — no extra passphrase."
        );
    }
    Ok(())
}

// ---------- leave ----------

/// Leave the puddle on this envoy. Per ADR-021 (0.40.0), this is the
/// canonical lifecycle exit verb covering puddle + lockbox +
/// gitback cleanup.
///
/// Steps:
///   1. If lockbox is enrolled: invoke `lockbox leave --purge
///      --yes` internally — the lockbox daemon picks up the
///      `leave.pending` sentinel and fans out a signed LEAVE envelope
///      to live peers, then scrubs `~/.lockbox/` + `~/lockbox/`.
///   2. Lock the puddle session helper (best-effort).
///   3. Scrub `~/.vigo-puddle/` (the wrapped keypair, salt, pubkey).
///   4. Scrub `~/.vigo-gitback/` if present (per-project state for
///      every gitback project this user was in).
///
/// Idempotent on the dir-removal side. The lockbox LEAVE fanout is
/// asynchronous — peers may take up to one announce tick (~30s) to
/// observe the departure, but the local scrub returns immediately.
///
/// Flags:
///   --yes    Skip the confirmation prompt.
fn cmd_leave(args: &[String]) -> Result<()> {
    let mut yes = false;
    for a in args {
        match a.as_str() {
            "--yes" | "-y" => yes = true,
            "--help" | "-h" => {
                eprintln!(
                    "vigo swarm puddle leave — leave the puddle on this envoy\n\n\
                     USAGE:\n    vigo swarm puddle leave [--yes]\n\n\
                     Signs a LEAVE envelope for lockbox peers, locks the puddle\n\
                     session helper, and wipes ~/.vigo-puddle/, ~/.lockbox/,\n\
                     ~/lockbox/, and ~/.vigo-gitback/. Refuses without --yes."
                );
                return Ok(());
            }
            other if other.starts_with("--") => bail!("unknown flag: {}", other),
            _ => bail!("vigo swarm puddle leave takes no positional arguments; got {}", a),
        }
    }

    let home = user_home_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!(
            "puddle is not initialized for this user — nothing to leave. \
             (Was this user ever a puddle member on this envoy?)"
        );
    }

    if !yes {
        let pubkey = super::local_puddle_pubkey(&home)?
            .unwrap_or_else(|| "(unreadable)".to_string());
        eprintln!();
        eprintln!("This will leave the puddle on this envoy and wipe local state:");
        eprintln!("  puddle pubkey:    {}", pubkey);
        eprintln!("  ~/.vigo-puddle/   keypair, salt, pubkey, retired-puddles log");
        eprintln!("  ~/.lockbox/     wrapped age identity + recipients + cert");
        eprintln!("  ~/lockbox/      ciphertext at rest (recoverable from peers)");
        eprintln!("  ~/.vigo-gitback/ per-project state for any gitback projects");
        eprintln!();
        eprintln!("Lockbox peers receive a signed LEAVE envelope on the next");
        eprintln!("announce tick (~30s). Gitback projects are not gossiped — admin");
        eprintln!("`gitback project revoke` is a separate cleanup step if needed.");
        eprintln!();
        eprintln!("Pass --yes to confirm.");
        return Ok(());
    }

    // 1. Lockbox leave (signs envelope, fans out, scrubs lockbox
    //    state). Best-effort: a failure here doesn't stop the puddle
    //    cleanup — the operator can always run `lockbox purge` later
    //    to finish the local wipe. Per ADR-021 (0.40.0), we drop the
    //    leave.pending sentinel directly via the lockbox helper
    //    function rather than going through a public `lockbox leave`
    //    verb (which retired in 0.40.0).
    let cs_identity = home.join(".lockbox").join("identity.age");
    if cs_identity.exists() {
        let username = user_for_invocation()?;
        println!("Signing lockbox LEAVE envelope and scrubbing ciphertext...");
        match crate::swarm::lockbox::cli::enqueue_leave(
            &home,
            &username,
            /* keep_identity */ false,
            /* purge_content */ true,
        ) {
            Ok(()) => {
                println!("✓ lockbox leave queued (peers learn within ~30s)");
            }
            Err(e) => {
                eprintln!("warning: lockbox leave failed: {:#}", e);
                eprintln!("         continuing with puddle cleanup anyway");
            }
        }
    }

    // 2. Lock the puddle session helper. Best-effort — already-locked
    //    is fine.
    let _ = session::request_lock();

    // 3. Scrub ~/.vigo-puddle/.
    let puddle_dot = config::dot_dir(&home);
    if puddle_dot.exists() {
        match fs::remove_dir_all(&puddle_dot) {
            Ok(()) => println!("✓ scrubbed {}", puddle_dot.display()),
            Err(e) => {
                eprintln!(
                    "warning: failed to remove {}: {} (manual cleanup needed)",
                    puddle_dot.display(),
                    e
                );
            }
        }
    }

    // 4. Scrub ~/.vigo-gitback/ — per-project state for every
    //    gitback project this user was a member of. Bare repos
    //    under /var/lib/vigo/swarm/gitback/ are root-owned and
    //    survive — admin cleanup is a separate path.
    let gm_user_dir = home.join(".vigo-gitback");
    if gm_user_dir.exists() {
        match fs::remove_dir_all(&gm_user_dir) {
            Ok(()) => println!("✓ scrubbed {}", gm_user_dir.display()),
            Err(e) => {
                eprintln!(
                    "warning: failed to remove {}: {} (manual cleanup needed)",
                    gm_user_dir.display(),
                    e
                );
            }
        }
    }

    println!();
    println!("Puddle membership ended on this envoy. To re-join later:");
    println!("  on a current puddle member: vigo swarm puddle pair");
    println!("  on this envoy:              sudo vigo swarm puddle join --from <host>");
    Ok(())
}

// ---------- rekey-pair ----------

/// Founder-side: rotate the puddle keypair and issue a 5-minute claim
/// code. On the first call inside the 24h graceful window, generates
/// a new keypair, atomic-swaps the local identity files, runs the
/// lockbox rewrap, and records the retirement entry. On subsequent
/// calls inside the same window, only a fresh claim code is issued —
/// the keypair and lockbox state are not rotated again.
///
/// Flags:
///   --close             Close the active rekey window immediately. No
///                       new claim code is issued; existing rekey.pending
///                       sentinel is cleared.
///   --window-secs <N>   Override the default 24h graceful window.
fn cmd_rekey_pair(args: &[String]) -> Result<()> {
    let mut close = false;
    let mut window_secs: i64 = rekey::REKEY_GRACEFUL_SECS;
    let mut force_orphan_projects = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--close" => close = true,
            "--window-secs" => {
                i += 1;
                let val = args
                    .get(i)
                    .context("--window-secs requires an integer (seconds)")?;
                window_secs = val
                    .parse::<i64>()
                    .with_context(|| format!("parsing --window-secs={}", val))?;
                if window_secs < 60 {
                    bail!("--window-secs must be at least 60");
                }
            }
            // ADR-019: when a gitback project's delegation chain has
            // hit DELEGATION_MAX_LEN, the pre-flight check refuses to
            // proceed. This flag opts into "rekey anyway, mark those
            // projects frozen-at-rekey" — operator's escape hatch for
            // when they don't care about the project.
            "--force-orphan-projects" => force_orphan_projects = true,
            s if s.starts_with("--") => bail!("unknown flag: {}", s),
            s => bail!("unexpected positional argument: {}", s),
        }
        i += 1;
    }

    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!("puddle not initialized on this envoy — run `init` or `join` first");
    }

    if close {
        let now = now_secs();
        let adjusted = rekey::close_active_session(&home, &username, now)?;
        rekey::clear_pending(&home);
        if adjusted == 0 {
            println!("No active rekey window — nothing to close.");
        } else {
            println!("✓ Rekey window closed. Surviving envoys can no longer claim under the");
            println!(
                "  retired puddle identity. Run `rekey-pair` again to start a new rotation."
            );
        }
        return Ok(());
    }

    let entries = rekey::load_retired(&home)?;
    let now = now_secs();
    let active = rekey::active_session(&entries, now);

    if let Some(active_entry) = active {
        // In-flight rotation — reissue a fresh claim code without
        // rotating the keypair again. Surviving envoys that haven't
        // claimed yet need a new short-lived code.
        let code = codes::generate_code();
        rekey::write_pending(&home, &code, &username)?;
        let remaining = (active_entry.expires_at - now).max(0);
        let hours = remaining / 3600;
        let mins = (remaining % 3600) / 60;
        println!();
        println!("Reissued rekey claim code (rotation already in progress)");
        println!("  Claim code:      {}", code);
        println!(
            "  Code valid for:  5 minutes ({} seconds)",
            codes::CLAIM_TTL_SECS
        );
        println!(
            "  Window remaining: {}h {}m before founder closes the rekey session",
            hours, mins
        );
        println!();
        println!("On the surviving envoy, run:");
        println!(
            "    sudo vigo swarm puddle rekey --from {}",
            local_hostname_or("<this-host>")
        );
        println!("then enter the claim code when prompted, then enter your puddle passphrase.");
        return Ok(());
    }

    // Fresh rotation. Verify passphrase first by unwrapping the
    // existing identity — fail loudly before any on-disk mutation.
    println!(
        "Rotating puddle keypair. The puddle passphrase is required to authorize this."
    );
    let passphrase = prompt_password(false)?;
    if passphrase.is_empty() {
        bail!("passphrase must not be empty");
    }
    let old_blob = fs::read(config::wrapped_path(&home))
        .with_context(|| format!("reading {}", config::wrapped_path(&home).display()))?;
    let old_salt = fs::read(config::salt_path(&home))
        .with_context(|| format!("reading {}", config::salt_path(&home).display()))?;
    let old_key = unwrap_signing_key(&old_blob, passphrase.as_bytes(), &old_salt)
        .context("unwrap with provided passphrase — passphrase rejected")?;
    let old_pubkey_text = encode_sign_pubkey(&old_key.verifying_key());

    // Generate the new keypair + fresh salt; wrap under the same
    // passphrase so surviving envoys keep the single-passphrase UX.
    let new_key = generate_signing_keypair();
    let new_pubkey_text = encode_sign_pubkey(&new_key.verifying_key());
    let new_salt = crate::swarm::lockbox::crypt::fresh_salt();
    let new_blob = wrap_signing_key(&new_key, passphrase.as_bytes(), &new_salt)
        .context("wrapping new puddle identity")?;

    // ADR-019 pre-flight: check every gitback project the user
    // founded against the delegation cap. Aborts here BEFORE any
    // keypair mutation when a project would push past the cap and
    // the operator hasn't passed --force-orphan-projects.
    let pre_flight = crate::swarm::gitback::rekey::pre_flight_check_caps(
        &home,
        &old_pubkey_text,
    )
    .context("gitback pre-flight cap check")?;
    if !pre_flight.at_cap.is_empty() && !force_orphan_projects {
        bail!(
            "{} gitback project(s) at delegation cap ({}/{}): {}\n  \
             Run `vigo swarm gitback project re-found <handle>` per project to \
             mint a fresh project_id, or pass --force-orphan-projects to proceed \
             and mark these projects frozen-at-rekey.",
            pre_flight.at_cap.len(),
            crate::swarm::anchor::envelopes::DELEGATION_MAX_LEN,
            crate::swarm::anchor::envelopes::DELEGATION_MAX_LEN,
            pre_flight.at_cap.join(", ")
        );
    }

    // Atomic swap: rename current files to `.pre-rekey-<ts>` backups,
    // then write the new envelopes. If the writes fail we restore
    // from backup so the user is never left without a working
    // identity.wrapped/.salt/.pub trio.
    let now = now_secs();
    let backup = |p: &Path| -> PathBuf {
        let stem = p.file_name().and_then(|s| s.to_str()).unwrap_or("file");
        p.with_file_name(format!("{}.pre-rekey-{}", stem, now))
    };
    let wp = config::wrapped_path(&home);
    let sp = config::salt_path(&home);
    let pp = config::pubkey_path(&home);
    let wp_bak = backup(&wp);
    let sp_bak = backup(&sp);
    let pp_bak = backup(&pp);
    fs::rename(&wp, &wp_bak)
        .with_context(|| format!("backup {} → {}", wp.display(), wp_bak.display()))?;
    fs::rename(&sp, &sp_bak)
        .with_context(|| format!("backup {} → {}", sp.display(), sp_bak.display()))?;
    fs::rename(&pp, &pp_bak)
        .with_context(|| format!("backup {} → {}", pp.display(), pp_bak.display()))?;
    let restore = || {
        let _ = fs::rename(&wp_bak, &wp);
        let _ = fs::rename(&sp_bak, &sp);
        let _ = fs::rename(&pp_bak, &pp);
    };
    if let Err(e) = write_private(&wp, &new_blob, &username)
        .and_then(|()| write_private(&sp, &new_salt, &username))
        .and_then(|()| write_pubkey(&pp, &new_pubkey_text, &username))
    {
        restore();
        return Err(e.context("writing new puddle identity — restored from backup"));
    }

    // Persist retirement entry before the lockbox sync. If the
    // rewrap fails the founder still has a record of "we rotated to
    // X at T" which the operator can reconcile manually.
    rekey::record_retirement(
        &home,
        &username,
        &old_pubkey_text,
        &new_pubkey_text,
        now,
        window_secs,
    )?;

    // Lockbox rewrap + TOFU pin clear + recipients regen. Logged
    // outcome — not fatal for the rotation if the user has no
    // lockbox state.
    let outcome = rekey::apply_post_rotation_lockbox_sync(
        &home,
        &username,
        &old_key,
        &new_key,
        &old_pubkey_text,
    )
    .context("lockbox sync after rotation")?;

    // ADR-019: gitback rekey sync — sign + append delegations under
    // OLD for every project the user founded, rebuild tokens.json
    // (chain[0] bootstrap + fresh admin under NEW), rewrite member
    // entries OLD → NEW. Best-effort with per-project failure
    // tracking; the rotation itself has already committed.
    let gm_outcome =
        crate::swarm::gitback::rekey::apply_post_rotation_gitback_sync(
            &home,
            &old_key,
            &new_key,
            &old_pubkey_text,
            &new_pubkey_text,
            now,
            force_orphan_projects,
        )
        .context("gitback sync after rotation")?;

    // Restart the session helper with the new key so sibling
    // subsystems pick up the rotation immediately. Lock first
    // (idempotent), then spawn a fresh helper — same shape `unlock`
    // uses.
    let _ = session::request_lock();
    std::thread::sleep(std::time::Duration::from_millis(150));
    let parent_pid =
        resolve_shell_pid().unwrap_or_else(|| unsafe { libc::getppid() } as u32);
    if let Err(e) =
        session::spawn_helper(&new_key, parent_pid, session::DEFAULT_IDLE_MINUTES, &home)
    {
        eprintln!("warning: failed to restart puddle session helper: {:#}", e);
        eprintln!(
            "         the rotation succeeded on disk; run `vigo swarm puddle unlock` manually."
        );
    }

    // Issue the first claim code for surviving envoys.
    let code = codes::generate_code();
    rekey::write_pending(&home, &code, &username)?;

    println!();
    println!("✓ Puddle keypair rotated.");
    println!("  Old puddle pubkey: {}", old_pubkey_text);
    println!("  New puddle pubkey: {}", new_pubkey_text);
    println!(
        "  Backups (30d):    {}, {}, {}",
        wp_bak.display(),
        sp_bak.display(),
        pp_bak.display()
    );
    print_rewrap_outcome(outcome);
    print_gitback_rekey_outcome(&gm_outcome);
    println!();
    println!(
        "  Rekey window:     {}h ({}s) before founder closes the session",
        window_secs / 3600,
        window_secs
    );
    println!("  Claim code:       {}", code);
    println!(
        "  Code valid for:   5 minutes ({} seconds)",
        codes::CLAIM_TTL_SECS
    );
    println!();
    println!("On each surviving envoy, run:");
    println!(
        "    sudo vigo swarm puddle rekey --from {}",
        local_hostname_or("<this-host>")
    );
    println!("then enter the claim code when prompted, then enter your puddle passphrase.");
    println!();
    println!(
        "Re-run `rekey-pair` (no flags) inside the {}h window to issue another code without",
        window_secs / 3600
    );
    println!("re-rotating the keypair. Run `rekey-pair --close` to close the window early.");
    Ok(())
}

fn print_gitback_rekey_outcome(
    outcome: &crate::swarm::gitback::rekey::GitbackRekeyOutcome,
) {
    if outcome.delegations_signed.is_empty()
        && outcome.members_updated.is_empty()
        && outcome.tokens_rebuilt.is_empty()
        && outcome.failures.is_empty()
        && outcome.in_warn_band.is_empty()
    {
        println!("  Gitback sync:   skipped (no projects under ~/.vigo-gitback)");
        return;
    }
    if !outcome.delegations_signed.is_empty() {
        println!(
            "  Gitback sync:   delegated {} project(s): {}",
            outcome.delegations_signed.len(),
            outcome.delegations_signed.join(", ")
        );
    }
    if !outcome.tokens_rebuilt.is_empty() {
        println!(
            "                    tokens.json rebuilt on {} founded project(s): {}",
            outcome.tokens_rebuilt.len(),
            outcome.tokens_rebuilt.join(", ")
        );
    }
    if !outcome.members_updated.is_empty() {
        println!(
            "                    member entry rewritten on {} project(s): {}",
            outcome.members_updated.len(),
            outcome.members_updated.join(", ")
        );
    }
    if !outcome.in_warn_band.is_empty() {
        println!(
            "                    ⚠ {} project(s) at chain length ≥ {}: {}",
            outcome.in_warn_band.len(),
            crate::swarm::anchor::envelopes::DELEGATION_WARN_LEN,
            outcome.in_warn_band.join(", ")
        );
        println!(
            "                       consider `vigo swarm gitback project re-found <handle>` for hygiene"
        );
    }
    if !outcome.failures.is_empty() {
        println!(
            "                    ✗ {} project(s) had issues:",
            outcome.failures.len()
        );
        for (handle, err) in &outcome.failures {
            println!("                       - {}: {}", handle, err);
        }
    }
}

fn print_rewrap_outcome(outcome: crate::swarm::lockbox::identity_rewrap::RewrapOutcome) {
    use crate::swarm::lockbox::identity_rewrap::RewrapOutcome;
    match outcome {
        RewrapOutcome::NotEnrolled => {
            println!("  Lockbox rewrap: skipped (not enrolled on this envoy)");
        }
        RewrapOutcome::Rewrapped { backup_path } => {
            println!(
                "  Lockbox rewrap: ok (backup at {})",
                backup_path.display()
            );
        }
    }
}

// ---------- rekey ----------

/// Joiner-side: claim a new wrapped puddle blob from the founder and
/// migrate this envoy onto it.
///
/// Flags:
///   --from <host>                Hostname of the founder envoy that ran rekey-pair.
///   --force-orphan-projects      ADR-019 escape hatch — proceed even if any
///                                gitback project is at the delegation cap;
///                                affected projects are marked frozen-at-rekey.
fn cmd_rekey(args: &[String]) -> Result<()> {
    let mut from: Option<String> = None;
    let mut force_orphan_projects = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--from" => {
                i += 1;
                from = Some(args.get(i).context("--from requires a hostname")?.clone());
            }
            "--force-orphan-projects" => force_orphan_projects = true,
            s if s.starts_with("--") => bail!("unknown flag: {}", s),
            s => bail!("unexpected positional argument: {}", s),
        }
        i += 1;
    }
    let from = from.context("--from <host> is required")?;
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;

    if !super::is_initialized(&home) {
        bail!(
            "puddle not initialized on this envoy — run `join --from <host>` first \
             (rekey is for envoys already in the puddle)"
        );
    }

    println!(
        "Rekeying with {} for user {}.",
        from, username
    );
    println!(
        "This requires the OLD puddle passphrase to unwrap the current key, then the SAME"
    );
    println!("passphrase to unwrap the new key the founder issued.");

    // Unwrap the OLD puddle key locally first — required input for
    // the lockbox rewrap. The founder uses the same passphrase
    // (rekey-pair preserves it), so we ask once and use it twice.
    let passphrase = prompt_password(false)?;
    if passphrase.is_empty() {
        bail!("passphrase must not be empty");
    }
    let old_blob = fs::read(config::wrapped_path(&home))
        .with_context(|| format!("reading {}", config::wrapped_path(&home).display()))?;
    let old_salt = fs::read(config::salt_path(&home))
        .with_context(|| format!("reading {}", config::salt_path(&home).display()))?;
    let old_key = unwrap_signing_key(&old_blob, passphrase.as_bytes(), &old_salt)
        .context("unwrap of OLD identity with provided passphrase failed")?;
    let old_pubkey_text = encode_sign_pubkey(&old_key.verifying_key());

    let code = prompt_line("rekey claim code (NNNN-NNNN): ")?;
    if code.trim().is_empty() {
        bail!("claim code must not be empty");
    }

    let tls = load_envoy_tls().context(
        "puddle rekey needs the agent's mTLS key (0600 root-owned). \
         Run via sudo: `sudo vigo swarm puddle rekey --from <host>`",
    )?;
    let agent = build_mtls_agent(&tls)?;
    let url = format!(
        "https://{}:{}{}{}",
        from,
        crate::swarm::server::DEFAULT_PORT,
        rekey::REKEY_CLAIM_PREFIX,
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
        bail!("rekey-claim failed (HTTP {}): {}", code, msg.trim());
    }
    let mut resp = resp;
    let bytes = resp.body_mut().read_to_vec().context("reading response body")?;
    let (new_salt, new_blob) = codes::parse_response(&bytes)?;

    let new_key = unwrap_signing_key(&new_blob, passphrase.as_bytes(), &new_salt).context(
        "unwrap of NEW wrapped blob failed — the founder may have rekeyed with a different \
         passphrase. Reissue rekey-pair on the founder using the OLD passphrase, or \
         rotate-passphrase on the founder before retrying.",
    )?;
    let new_pubkey_text = encode_sign_pubkey(&new_key.verifying_key());

    if new_pubkey_text == old_pubkey_text {
        bail!(
            "the founder's wrapped blob unwraps to the SAME puddle pubkey we already have — \
             nothing to rotate. The founder hasn't run `rekey-pair` since last we rotated."
        );
    }

    // ADR-019 pre-flight: same shape as cmd_rekey_pair. Founder's
    // rekey-pair already cleared the cap on their envoy; this
    // envoy's local state should match unless this envoy has been
    // offline long enough to fall behind on prior rekeys.
    let pre_flight = crate::swarm::gitback::rekey::pre_flight_check_caps(
        &home,
        &old_pubkey_text,
    )
    .context("gitback pre-flight cap check")?;
    if !pre_flight.at_cap.is_empty() && !force_orphan_projects {
        bail!(
            "{} gitback project(s) at delegation cap ({}/{}): {}\n  \
             Run `vigo swarm gitback project re-found <handle>` per project to \
             mint a fresh project_id, or pass --force-orphan-projects to proceed \
             and mark these projects frozen-at-rekey.",
            pre_flight.at_cap.len(),
            crate::swarm::anchor::envelopes::DELEGATION_MAX_LEN,
            crate::swarm::anchor::envelopes::DELEGATION_MAX_LEN,
            pre_flight.at_cap.join(", ")
        );
    }

    // Atomic swap of local files: backup OLD as `.pre-rekey-<ts>`,
    // write new envelopes. Restore on failure.
    let now = now_secs();
    let backup = |p: &Path| -> PathBuf {
        let stem = p.file_name().and_then(|s| s.to_str()).unwrap_or("file");
        p.with_file_name(format!("{}.pre-rekey-{}", stem, now))
    };
    let wp = config::wrapped_path(&home);
    let sp = config::salt_path(&home);
    let pp = config::pubkey_path(&home);
    let wp_bak = backup(&wp);
    let sp_bak = backup(&sp);
    let pp_bak = backup(&pp);
    fs::rename(&wp, &wp_bak)
        .with_context(|| format!("backup {} → {}", wp.display(), wp_bak.display()))?;
    fs::rename(&sp, &sp_bak)
        .with_context(|| format!("backup {} → {}", sp.display(), sp_bak.display()))?;
    fs::rename(&pp, &pp_bak)
        .with_context(|| format!("backup {} → {}", pp.display(), pp_bak.display()))?;
    let restore = || {
        let _ = fs::rename(&wp_bak, &wp);
        let _ = fs::rename(&sp_bak, &sp);
        let _ = fs::rename(&pp_bak, &pp);
    };
    if let Err(e) = write_private(&wp, &new_blob, &username)
        .and_then(|()| write_private(&sp, &new_salt, &username))
        .and_then(|()| write_pubkey(&pp, &new_pubkey_text, &username))
    {
        restore();
        return Err(e.context("writing new puddle identity — restored from backup"));
    }

    // Local retirement log entry — useful for status output and
    // historical audit. Use the same window as the founder so a
    // future `rekey-pair --close` event on the founder doesn't make
    // the joiner's view inconsistent. The joiner's local entry is
    // primarily informational.
    rekey::record_retirement(
        &home,
        &username,
        &old_pubkey_text,
        &new_pubkey_text,
        now,
        rekey::REKEY_GRACEFUL_SECS,
    )?;

    let outcome = rekey::apply_post_rotation_lockbox_sync(
        &home,
        &username,
        &old_key,
        &new_key,
        &old_pubkey_text,
    )
    .context("lockbox sync after rotation")?;

    // ADR-019: gitback sync — same as the founder side. Each envoy
    // in the puddle signs its OWN delegation locally using its copy
    // of the OLD puddle key (which is the same shared keypair, so the
    // canonical signed bytes are identical modulo `delegated_at`)
    // and rebuilds tokens.json with the fresh admin under NEW. Each
    // envoy maintains a self-consistent local view.
    let gm_outcome =
        crate::swarm::gitback::rekey::apply_post_rotation_gitback_sync(
            &home,
            &old_key,
            &new_key,
            &old_pubkey_text,
            &new_pubkey_text,
            now,
            force_orphan_projects,
        )
        .context("gitback sync after rotation")?;

    // Lock any stale session helper; the user runs `unlock` in their
    // own (non-sudo) shell to start the new helper.
    let _ = session::request_lock();

    println!();
    println!("✓ Migrated to new puddle keypair.");
    println!("  Old puddle pubkey: {}", old_pubkey_text);
    println!("  New puddle pubkey: {}", new_pubkey_text);
    println!(
        "  Backups (30d):    {}, {}, {}",
        wp_bak.display(),
        sp_bak.display(),
        pp_bak.display()
    );
    print_rewrap_outcome(outcome);
    print_gitback_rekey_outcome(&gm_outcome);
    println!();
    println!(
        "Run `vigo swarm puddle unlock` (no sudo) to start the session helper under the"
    );
    println!("new puddle identity.");
    Ok(())
}

// ---------- members ----------

fn cmd_members(_args: &[String]) -> Result<()> {
    // Local-only view: this envoy's pubkey + initialization status.
    // ADR-015 (gitback substrate) and ADR-016 (lockbox membership
    // on puddle) shipped, so peer puddle pubkeys ARE observable —
    // but via the lockbox peer table, not the puddle module's own
    // state. Surface that path so an operator looking at the local
    // puddle view knows where to find the cross-envoy aggregate.
    let home = user_home_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!("puddle not initialized on this envoy");
    }
    let pubkey = super::local_puddle_pubkey(&home)?
        .ok_or_else(|| anyhow!("identity.pub missing despite is_initialized"))?;
    let host = local_hostname_or("<this-host>");
    println!("puddle members (this envoy's view):");
    println!("  {} (this envoy)  {}", host, pubkey);
    println!();
    println!(
        "This is the local view. For the cross-envoy puddle pubkey aggregate (which peers\n\
         this envoy has discovered via swarm announces), inspect `~/.lockbox/recipients.txt`\n\
         or run `vigocli swarm lockbox show <user>` from an admin shell for the server-\n\
         side fleet rollup."
    );
    Ok(())
}

// ---------- rotate-passphrase ----------

fn cmd_rotate_passphrase(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!("puddle not initialized on this envoy");
    }
    let blob = fs::read(config::wrapped_path(&home))?;
    let salt = fs::read(config::salt_path(&home))?;

    println!("Re-wrapping the local puddle key under a new passphrase.");
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

    let new_salt = crate::swarm::lockbox::crypt::fresh_salt();
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
    println!("✓ Local puddle key re-wrapped. Pubkey unchanged: {}",
        encode_sign_pubkey(&key.verifying_key()));
    println!("  Run `rotate-passphrase` on each other puddle envoy to rotate them too.");
    Ok(())
}

// ---------- status ----------

fn cmd_status(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation().ok();
    let dot = config::dot_dir(&home);
    println!("puddle status");
    println!("  home:             {}", home.display());
    println!("  dot_dir:          {}", dot.display());
    println!("  initialized:      {}", super::is_initialized(&home));
    let local_pubkey_text = if super::is_initialized(&home) {
        match super::local_puddle_pubkey(&home)? {
            Some(pk) => {
                println!("  puddle pubkey:     {}", pk);
                Some(pk)
            }
            None => {
                println!(
                    "  puddle pubkey:    (identity.pub missing) — local puddle state"
                );
                println!(
                    "                    is corrupt, so this envoy can't prove its"
                );
                println!(
                    "                    puddle membership to peers. Confirm the"
                );
                println!(
                    "                    expected pubkey on another puddle member"
                );
                println!(
                    "                    via `vigo swarm puddle status`, then"
                );
                println!(
                    "                    re-pair this envoy with"
                );
                println!(
                    "                    `vigo swarm puddle pair` / `join`."
                );
                None
            }
        }
    } else {
        None
    };
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
            _ => {
                println!(
                    "  pair pending:     yes (sentinel unparseable) — the pair-state"
                );
                println!(
                    "                    file is corrupt; cancel and re-issue with"
                );
                println!(
                    "                    `vigo swarm puddle pair`."
                );
            }
        }
    } else {
        println!("  pair pending:     no");
    }
    match session::request_status() {
        Ok(msg) => println!("  session helper:   {}", msg),
        Err(_) => println!("  session helper:   not running (locked)"),
    }
    print_rekey_status(&home);
    if let (Some(local_pk), Some(user)) = (local_pubkey_text, username) {
        print_offline_envoy_hint(&local_pk, &user);
    }
    Ok(())
}

fn print_rekey_status(home: &Path) {
    let entries = match rekey::load_retired(home) {
        Ok(e) => e,
        Err(e) => {
            println!(
                "  rekey state:      (unreadable: {}) — the retired-puddles", e
            );
            println!(
                "                    record is unparseable, so rekey history"
            );
            println!(
                "                    is unknown. Inspect"
            );
            println!(
                "                    `~/.vigo-puddle/retired_puddles.json`; if"
            );
            println!(
                "                    you can't repair it, file an issue —"
            );
            println!(
                "                    rekey state should never become invalid."
            );
            return;
        }
    };
    let now = now_secs();
    let active = rekey::active_session(&entries, now);
    if let Some(active_entry) = active {
        let remaining = (active_entry.expires_at - now).max(0);
        let hours = remaining / 3600;
        let mins = (remaining % 3600) / 60;
        println!(
            "  rekey window:     active ({}h {}m remaining)",
            hours, mins
        );
        println!(
            "  retired pubkey:   {}",
            short_pubkey(&active_entry.old_puddle_pubkey)
        );
        let rekey_pending = config::rekey_pending_path(home);
        if rekey_pending.exists() {
            match rekey::read_pending(home) {
                Ok(Some(spec)) => {
                    let claim_remaining = (spec.expires_at - now).max(0);
                    println!(
                        "  rekey claim code: pending (expires in {}s)",
                        claim_remaining
                    );
                }
                _ => {
                    println!(
                        "  rekey claim code: pending (sentinel unparseable) — the"
                    );
                    println!(
                        "                    claim-code file is corrupt; cancel"
                    );
                    println!(
                        "                    and re-issue with"
                    );
                    println!(
                        "                    `vigo swarm puddle rekey-pair`."
                    );
                }
            }
        } else {
            println!(
                "  rekey claim code: none — issue one with"
            );
            println!(
                "                    `vigo swarm puddle rekey-pair` if you want"
            );
            println!(
                "                    another envoy to claim the new puddle"
            );
            println!(
                "                    identity within the active rekey window."
            );
        }
    } else {
        println!("  rekey window:     none active");
    }
    let historical: Vec<&_> = entries
        .iter()
        .filter(|e| e.expires_at <= now)
        .collect();
    if !historical.is_empty() {
        let shown = historical.iter().rev().take(3);
        let total = historical.len();
        let suffix = if total > 3 {
            format!(" (+{} older in retired_puddles.json)", total - 3)
        } else {
            String::new()
        };
        println!("  retired puddles:   {} historical entry/entries{}", total, suffix);
        for entry in shown {
            let age_secs = now.saturating_sub(entry.retired_at);
            let age = humanize_duration(age_secs);
            println!(
                "                    - {} (rotated {} ago)",
                short_pubkey(&entry.old_puddle_pubkey),
                age
            );
        }
    }
}

/// Shorten a `ed25519:<hex>` form to first/last segments for readable
/// `status` output. Full value remains in the JSON file for matching.
fn short_pubkey(full: &str) -> String {
    if let Some(rest) = full.strip_prefix("ed25519:") {
        if rest.len() > 16 {
            return format!("ed25519:{}…{}", &rest[..8], &rest[rest.len() - 8..]);
        }
    }
    full.to_string()
}

fn humanize_duration(seconds: i64) -> String {
    let days = seconds / 86400;
    if days >= 1 {
        return format!("{}d", days);
    }
    let hours = seconds / 3600;
    if hours >= 1 {
        return format!("{}h", hours);
    }
    let mins = seconds / 60;
    if mins >= 1 {
        return format!("{}m", mins);
    }
    format!("{}s", seconds.max(0))
}

/// If any peer in the local lockbox peer table is announcing under
/// a puddle pubkey that doesn't match this envoy's local puddle pubkey,
/// this envoy was probably offline during a rekey on the founder.
/// Print a one-shot hint so the operator knows where to migrate from.
///
/// Comparison is on the bare hex (after stripping the `ed25519:`
/// prefix and lowercasing). lockbox state stores puddle_pubkey
/// without the prefix; puddle stores with the prefix in
/// identity.pub.
fn print_offline_envoy_hint(local_puddle_pubkey: &str, username: &str) {
    let local_hex = local_puddle_pubkey
        .strip_prefix("ed25519:")
        .unwrap_or(local_puddle_pubkey)
        .trim()
        .to_ascii_lowercase();
    if local_hex.is_empty() {
        return;
    }
    let table = crate::swarm::lockbox::state::PeerTable::load(username);
    let mut divergent: Vec<(String, String)> = Vec::new();
    for entry in table.peers.values() {
        if entry.puddle_pubkey.is_empty() {
            continue;
        }
        if entry.puddle_pubkey != local_hex {
            divergent.push((entry.hostname.clone(), entry.puddle_pubkey.clone()));
        }
    }
    if divergent.is_empty() {
        return;
    }
    divergent.sort();
    println!();
    println!("Note: peer(s) advertising a different puddle pubkey than this envoy:");
    for (host, pk) in &divergent {
        println!(
            "  {} → ed25519:{}",
            host,
            if pk.len() > 16 {
                format!("{}…{}", &pk[..8], &pk[pk.len() - 8..])
            } else {
                pk.clone()
            }
        );
    }
    println!(
        "If you rekeyed on {}, this envoy was offline at the time. Run:",
        divergent[0].0
    );
    println!("    sudo vigo swarm puddle rekey --from {}", divergent[0].0);
    println!(
        "to migrate. Otherwise the peer(s) may be on a puddle identity that's not yours — \
         investigate before rekeying."
    );
}

// ---------- name (friendly puddle names, ADR-022) ----------

fn print_name_usage() {
    eprintln!(
        "vigo swarm puddle name — friendly names for gitback projects (ADR-022)

USAGE:
    vigo swarm puddle name set <name>     Claim <name> for this puddle. Requires the
                                          puddle unlocked. Afterwards `gitback://<name>/<repo>`
                                          resolves to `gitback://sha256(your-pubkey ‖ <repo>)/<repo>`.
    vigo swarm puddle name clear [--yes]  Drop this puddle's name claim. Breaks every
                                          `gitback://<name>/…` URL; the hex project_id URLs keep working.
    vigo swarm puddle name show           Show this puddle's name claim and the fleet view of it.

Friendly names are first-come and cooperatively enforced — a hint, not a trust anchor.
The unforgeable form is always `gitback://<project_id>/<repo>`. See ADR-022."
    );
}

fn cmd_name(args: &[String]) -> Result<()> {
    let (sub, rest): (&str, &[String]) = match args.split_first() {
        Some((first, rest)) => (first.as_str(), rest),
        None => ("show", &[]),
    };
    match sub {
        "show" => cmd_name_show(rest),
        "set" => cmd_name_set(rest),
        "clear" => cmd_name_clear(rest),
        "help" | "--help" | "-h" => {
            print_name_usage();
            Ok(())
        }
        other => bail!(
            "unknown `vigo swarm puddle name` subcommand: {}\n\
             usage: vigo swarm puddle name {{set <name> | clear [--yes] | show}}",
            other
        ),
    }
}

/// Bare lowercase-hex form of this envoy's local puddle pubkey.
/// Errors if puddle isn't initialized or `identity.pub` is missing /
/// malformed.
fn local_puddle_pubkey_hex(home: &Path) -> Result<String> {
    let raw = super::local_puddle_pubkey(home)?.ok_or_else(|| {
        anyhow!(
            "puddle not initialized on this envoy — run `vigo swarm puddle init` \
             (or `vigo swarm puddle join --from <host>`) first"
        )
    })?;
    let hex = raw
        .strip_prefix("ed25519:")
        .unwrap_or(&raw)
        .trim()
        .to_ascii_lowercase();
    name::parse_pubkey_hex(&hex).context("local ~/.vigo-puddle/identity.pub is malformed")?;
    Ok(hex)
}

fn sig_to_array(v: &[u8]) -> Result<[u8; 64]> {
    if v.len() != 64 {
        bail!(
            "puddle session helper returned a {}-byte signature, expected 64",
            v.len()
        );
    }
    let mut a = [0u8; 64];
    a.copy_from_slice(v);
    Ok(a)
}

fn cmd_name_set(args: &[String]) -> Result<()> {
    let mut name_arg: Option<String> = None;
    for a in args {
        match a.as_str() {
            "--help" | "-h" => {
                print_name_usage();
                return Ok(());
            }
            other if other.starts_with('-') => bail!("unknown flag for `name set`: {}", other),
            other => {
                if name_arg.is_some() {
                    bail!("`name set` takes exactly one <name>; got an extra argument {:?}", other);
                }
                name_arg = Some(other.to_string());
            }
        }
    }
    let name_arg = name_arg.context("usage: vigo swarm puddle name set <name>")?;
    name::validate_name(&name_arg)?;

    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if !super::is_initialized(&home) {
        bail!(
            "puddle not initialized on this envoy — run `vigo swarm puddle init` \
             (or `vigo swarm puddle join --from <host>`) first"
        );
    }
    let my_hex = local_puddle_pubkey_hex(&home)?;

    // Refuse the *accidental* collision: someone else already holds
    // this name in our current fleet view. This cannot beat the
    // propagation window or a deliberate squatter — see ADR-022. A
    // stale read of the cache is acceptable: worst case we refuse a
    // name that's actually free, and the operator retries after the
    // next refresh.
    let fleet = names_cache::FleetNameMap::load().unwrap_or_default();
    if let Some(other) = fleet.claimed_by_other(&name_arg, &my_hex) {
        bail!(
            "name '{}' is already claimed by puddle ed25519:{} in this fleet's current view.\n\
             Pick another name. If you believe that claim is stale (the other puddle ran \
             `name clear`), wait for the fleet name map to refresh and retry.\n\
             The unforgeable form is always gitback://<project_id>/<repo>.",
            name_arg, other
        );
    }

    // Carry a re-found supersedes chain forward only when re-issuing
    // the *same* name under the *same* anchor; any other `name set`
    // starts a fresh (empty) chain.
    let prior = name::NameClaim::load(&home).ok().flatten();
    let (supersedes, reissuing) = match &prior {
        Some(p)
            if p.body.name == name_arg && p.body.puddle_pubkey.eq_ignore_ascii_case(&my_hex) =>
        {
            (p.body.supersedes.clone(), true)
        }
        Some(p) if p.body.name != name_arg => {
            println!(
                "Replacing this puddle's existing name claim '{}' → '{}'.",
                p.body.name, name_arg
            );
            (Vec::new(), false)
        }
        _ => (Vec::new(), false),
    };
    if reissuing {
        println!("Re-issuing the existing name claim '{}'.", name_arg);
    }

    let body = name::NameClaimBody {
        puddle_pubkey: my_hex.clone(),
        name: name_arg.clone(),
        claimed_at: now_secs(),
        supersedes,
    };
    let claim = name::NameClaim::issue_with(body, |msg| {
        let sig = session::request_sign(msg).context(
            "the puddle session helper isn't running — run `vigo swarm puddle unlock` first",
        )?;
        sig_to_array(&sig)
    })?;
    claim.save(&home, &username)?;

    let pk_short = if my_hex.len() > 16 {
        format!("{}…", &my_hex[..16])
    } else {
        my_hex.clone()
    };
    println!("✓ Claimed puddle name '{}'", name_arg);
    println!("  Puddle pubkey: ed25519:{}", my_hex);
    println!(
        "  Resolves:      gitback://{}/<repo>  →  gitback://{}/<repo>  (project_id = sha256(pubkey ‖ repo))",
        name_arg, pk_short
    );
    println!();
    println!(
        "Cooperative first-come naming — a hint, not a trust anchor. gitback://{}/<repo>",
        name_arg
    );
    println!("resolves from this envoy immediately; other envoys pick it up as the fleet name");
    println!("map refreshes (next check-in + ~15 min). The hex form is always unambiguous.");
    Ok(())
}

fn cmd_name_clear(args: &[String]) -> Result<()> {
    let mut yes = false;
    for a in args {
        match a.as_str() {
            "--yes" | "-y" => yes = true,
            "--help" | "-h" => {
                print_name_usage();
                return Ok(());
            }
            other => bail!("unknown flag for `name clear`: {}", other),
        }
    }
    let home = user_home_for_invocation()?;
    let Some(claim) = name::NameClaim::load(&home).ok().flatten() else {
        println!("No name claim on this envoy — nothing to clear.");
        return Ok(());
    };
    if !yes {
        eprintln!(
            "Clearing this puddle's name claim '{name}'.\n\n\
             Every `gitback://{name}/<repo>` URL anyone wrote down will stop resolving — \
             and if a different puddle later claims '{name}', those URLs would resolve to \
             *their* projects instead. The hex `gitback://<project_id>/<repo>` URLs keep \
             working. Name claims do not expire on their own; clearing is the only way to \
             give the name back.\n\n\
             Re-run with --yes to proceed.",
            name = claim.body.name
        );
        bail!("refused without --yes");
    }
    name::NameClaim::clear(&home)?;
    println!("✓ Cleared puddle name claim '{}'.", claim.body.name);
    println!("  The fleet name map drops it on the next agent check-in cycle.");
    Ok(())
}

fn cmd_name_show(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    println!("puddle name (ADR-022)");
    let local_claim = match name::NameClaim::load(&home) {
        Ok(Some(claim)) => {
            println!("  claimed name:     {}", claim.body.name);
            println!("  puddle pubkey:    ed25519:{}", claim.body.puddle_pubkey);
            println!("  claimed at:       {} (unix seconds)", claim.body.claimed_at);
            if claim.body.supersedes.is_empty() {
                println!("  supersedes:       (none — original claim)");
            } else {
                println!(
                    "  supersedes:       {} delegation link(s) (re-found chain)",
                    claim.body.supersedes.len()
                );
            }
            match claim.verify_signature() {
                Ok(()) => println!("  signature:        ok"),
                Err(e) => println!(
                    "  signature:        INVALID ({}) — re-run `name set {}` to re-issue",
                    e, claim.body.name
                ),
            }
            Some(claim)
        }
        Ok(None) => {
            println!(
                "  claimed name:     (none) — run `vigo swarm puddle name set <name>` to claim one"
            );
            None
        }
        Err(e) => {
            println!(
                "  claimed name:     (unreadable: {}) — name-claim.json is corrupt; re-run `name set <name>`",
                e
            );
            None
        }
    };

    println!();
    println!("  fleet name map:   {}", names_cache::names_cache_path().display());
    match names_cache::FleetNameMap::load() {
        Ok(map) => {
            if map.updated_at > 0 {
                println!("  last refreshed:   {} (unix seconds)", map.updated_at);
            } else {
                println!("  last refreshed:   (never — agent hasn't fetched it yet)");
            }
            println!("  names known:      {}", map.names.len());
            if let Some(claim) = &local_claim {
                let claimants = map.verified_claims(&claim.body.name);
                if claimants.is_empty() {
                    println!(
                        "  fleet view '{}': not yet visible (agents haven't propagated your claim)",
                        claim.body.name
                    );
                } else if claimants.len() == 1 {
                    if claimants[0]
                        .0
                        .body
                        .puddle_pubkey
                        .eq_ignore_ascii_case(&claim.body.puddle_pubkey)
                    {
                        println!("  fleet view '{}': yours, unambiguous ✓", claim.body.name);
                    } else {
                        println!(
                            "  fleet view '{}': claimed by ed25519:{} — NOT yours",
                            claim.body.name, claimants[0].0.body.puddle_pubkey
                        );
                    }
                } else {
                    println!(
                        "  fleet view '{}': COLLISION — {} puddles claim it; resolvers will refuse it and require gitback://<project_id>/<repo>:",
                        claim.body.name, claimants.len()
                    );
                    for (c, _) in &claimants {
                        println!("                    - ed25519:{}", c.body.puddle_pubkey);
                    }
                }
            }
        }
        Err(e) => println!("  fleet name map:   (unreadable: {})", e),
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
    crate::swarm::lockbox::home_for_user_pub(&username)
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
    let pw = read_one_password("puddle passphrase: ")?;
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
    crate::swarm::lockbox::chown_to_user_pub(path, username)
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
    crate::swarm::lockbox::chown_to_user_pub(path, username)
        .with_context(|| format!("chown {} to {}", path.display(), username))?;
    Ok(())
}

#[cfg(not(unix))]
fn write_pubkey(path: &Path, pubkey: &str, _username: &str) -> Result<()> {
    fs::write(path, format!("{}\n", pubkey))
        .with_context(|| format!("writing {}", path.display()))
}

/// Atomically write `content` to `path` with mode 0644, chowned to
/// `username` when running as root. Write-to-temp-then-rename so a
/// concurrent reader (the `gitback` traits collector) never sees a
/// torn file. Used for public-by-construction per-user JSON — i.e.
/// the name claim ([`super::name::NameClaim::save`]).
#[cfg(unix)]
pub(super) fn write_user_file_0644(path: &Path, content: &[u8], username: &str) -> Result<()> {
    use std::os::unix::fs::OpenOptionsExt;
    let dir = path
        .parent()
        .ok_or_else(|| anyhow!("{} has no parent directory", path.display()))?;
    let file_name = path
        .file_name()
        .and_then(|n| n.to_str())
        .ok_or_else(|| anyhow!("{} has no file name", path.display()))?;
    let tmp = dir.join(format!(".{}.tmp.{}", file_name, std::process::id()));
    {
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o644)
            .open(&tmp)
            .with_context(|| format!("creating {}", tmp.display()))?;
        f.write_all(content)?;
        f.sync_all().ok();
    }
    crate::swarm::lockbox::chown_to_user_pub(&tmp, username)
        .with_context(|| format!("chown {} to {}", tmp.display(), username))?;
    fs::rename(&tmp, path)
        .with_context(|| format!("rename {} -> {}", tmp.display(), path.display()))?;
    Ok(())
}

#[cfg(not(unix))]
pub(super) fn write_user_file_0644(path: &Path, content: &[u8], _username: &str) -> Result<()> {
    fs::write(path, content).with_context(|| format!("writing {}", path.display()))
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
    crate::swarm::lockbox::chown_to_user_pub(path, username)
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
    crate::swarm::lockbox::pusher::build_agent_for_mod(tls)
}

// Defensive — silence dead-code warnings while the feature lands.
#[allow(dead_code)]
fn _types_check_marker(_k: &SigningKey) {
    let _ = SIGN_PUBKEY_LEN;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fmt_idle_humanizes_minutes() {
        assert_eq!(fmt_idle(session::DEFAULT_IDLE_MINUTES), "24h");
        assert_eq!(fmt_idle(60), "1h");
        assert_eq!(fmt_idle(120), "2h");
        assert_eq!(fmt_idle(30), "30m");
        assert_eq!(fmt_idle(90), "90m");
        assert_eq!(fmt_idle(1), "1m");
    }
}
