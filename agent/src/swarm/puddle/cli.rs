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
use super::pair;
use super::rekey;
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
    leave [--yes]              Leave the puddle on this envoy: sign LEAVE for cryptsync
                               peers, scrub ~/.vigo-puddle/, ~/.cryptsync/, ~/cryptsync/,
                               and ~/.vigo-gitmirror/.

REKEY (ADR-018, puddle keypair rotation):
    rekey-pair                 Rotate the puddle keypair and issue a 5-minute claim code.
    rekey-pair --close         Close the active 24h rekey window early.
    rekey --from <host>        Migrate this envoy to the founder's new puddle keypair.

DAILY:
    unlock                     Unwrap the key and start the session helper.
    lock                       Scrub the held key and stop the session helper.

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
    let salt = crate::swarm::cryptsync::crypt::fresh_salt();
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

fn cmd_unlock(_args: &[String]) -> Result<()> {
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
    session::spawn_helper(&key, parent_pid, DEFAULT_IDLE_MINUTES, &home)?;

    println!(
        "✓ Unlocked. Puddle session helper running until logout, lock, or {} min idle.",
        DEFAULT_IDLE_MINUTES
    );

    // ADR-016 slice 4: now that the puddle session helper is
    // running, this is the one moment at which a fresh envoy can
    // bootstrap cryptsync without the user having to type a second
    // passphrase — puddle-mode wrap-key derivation goes through the
    // helper we just spawned. Two paths:
    //
    //   - Identity missing → run `cryptsync puddle init` to create the
    //     per-envoy age key, recipients, and puddle-membership cert.
    //   - Identity present + wrap_mode=Puddle → run `cryptsync unlock`
    //     to start the cryptsync session helper. Without this step,
    //     the auto-encrypt watcher queues plaintext drops indefinitely
    //     because it can't derive the wrap key without the cryptsync
    //     helper running, even though the puddle helper has the
    //     material. Operator hit this on every `puddle unlock` after
    //     the first one until 0.39.6.
    //   - Identity present + wrap_mode=Standalone → skip. Standalone
    //     mode expects an interactive passphrase prompt that the
    //     puddle-unlock flow shouldn't surprise the user with.
    //
    // Failures don't undo the unlock — we surface the error and keep
    // going so the operator can re-run manually.
    let cs_dot = home.join(".cryptsync");
    let cs_identity = cs_dot.join("identity.age");
    let mut bootstrap_ok = true;
    if !cs_identity.exists() {
        println!();
        println!("Bootstrapping cryptsync (puddle-mode wrap, no extra passphrase)...");
        match crate::swarm::cryptsync::cli::run(vec![
            "puddle".to_string(),
            "init".to_string(),
        ]) {
            Ok(()) => {}
            Err(e) => {
                eprintln!("warning: cryptsync auto-init failed: {:#}", e);
                eprintln!(
                    "         re-run manually: `vigo swarm cryptsync puddle init`"
                );
                bootstrap_ok = false;
            }
        }
    }
    // Whether cryptsync was already enrolled or we just bootstrapped
    // it, start its session helper so encrypt / decrypt / list verbs
    // work immediately. Pre-0.41.2 only auto-started in the
    // already-enrolled branch, leaving freshly-bootstrapped envoys
    // half-ready (operator could encrypt but not decrypt until they
    // ran `cryptsync unlock` manually). Skip when bootstrap failed —
    // there's no identity to unlock and the operator needs to fix
    // the bootstrap first.
    if bootstrap_ok {
        println!();
        println!("Starting cryptsync session helper (puddle-derived wrap, no extra passphrase)...");
        match crate::swarm::cryptsync::cli::run(vec!["unlock".to_string()]) {
            Ok(()) => {}
            Err(e) => {
                eprintln!("warning: cryptsync unlock failed: {:#}", e);
                eprintln!(
                    "         re-run manually: `vigo swarm cryptsync unlock`"
                );
            }
        }
    }
    Ok(())
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
    // cryptsync on this envoy if its identity.age is still missing.
    // The session helper isn't running yet (`join` only persists the
    // wrapped blob; the user activates it on their own login), so we
    // don't run cryptsync init inline here — surface the side effect
    // the operator should expect from the unlock that follows.
    let cs_identity = home.join(".cryptsync").join("identity.age");
    if !cs_identity.exists() {
        println!();
        println!(
            "note: the next `puddle unlock` on this envoy will also bootstrap cryptsync \
             (encrypted P2P sync), wrapped under your puddle identity — no extra passphrase."
        );
    }
    Ok(())
}

// ---------- leave ----------

/// Leave the puddle on this envoy. Per ADR-021 (0.40.0), this is the
/// canonical lifecycle exit verb covering puddle + cryptsync +
/// gitmirror cleanup.
///
/// Steps:
///   1. If cryptsync is enrolled: invoke `cryptsync leave --purge
///      --yes` internally — the cryptsync daemon picks up the
///      `leave.pending` sentinel and fans out a signed LEAVE envelope
///      to live peers, then scrubs `~/.cryptsync/` + `~/cryptsync/`.
///   2. Lock the puddle session helper (best-effort).
///   3. Scrub `~/.vigo-puddle/` (the wrapped keypair, salt, pubkey).
///   4. Scrub `~/.vigo-gitmirror/` if present (per-project state for
///      every gitmirror project this user was in).
///
/// Idempotent on the dir-removal side. The cryptsync LEAVE fanout is
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
                     Signs a LEAVE envelope for cryptsync peers, locks the puddle\n\
                     session helper, and wipes ~/.vigo-puddle/, ~/.cryptsync/,\n\
                     ~/cryptsync/, and ~/.vigo-gitmirror/. Refuses without --yes."
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
        eprintln!("  ~/.cryptsync/     wrapped age identity + recipients + cert");
        eprintln!("  ~/cryptsync/      ciphertext at rest (recoverable from peers)");
        eprintln!("  ~/.vigo-gitmirror/ per-project state for any gitmirror projects");
        eprintln!();
        eprintln!("Cryptsync peers receive a signed LEAVE envelope on the next");
        eprintln!("announce tick (~30s). Gitmirror projects are not gossiped — admin");
        eprintln!("`gitmirror project revoke` is a separate cleanup step if needed.");
        eprintln!();
        eprintln!("Pass --yes to confirm.");
        return Ok(());
    }

    // 1. Cryptsync leave (signs envelope, fans out, scrubs cryptsync
    //    state). Best-effort: a failure here doesn't stop the puddle
    //    cleanup — the operator can always run `cryptsync purge` later
    //    to finish the local wipe. Per ADR-021 (0.40.0), we drop the
    //    leave.pending sentinel directly via the cryptsync helper
    //    function rather than going through a public `cryptsync leave`
    //    verb (which retired in 0.40.0).
    let cs_identity = home.join(".cryptsync").join("identity.age");
    if cs_identity.exists() {
        let username = user_for_invocation()?;
        println!("Signing cryptsync LEAVE envelope and scrubbing ciphertext...");
        match crate::swarm::cryptsync::cli::enqueue_leave(
            &home,
            &username,
            /* keep_identity */ false,
            /* purge_content */ true,
        ) {
            Ok(()) => {
                println!("✓ cryptsync leave queued (peers learn within ~30s)");
            }
            Err(e) => {
                eprintln!("warning: cryptsync leave failed: {:#}", e);
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

    // 4. Scrub ~/.vigo-gitmirror/ — per-project state for every
    //    gitmirror project this user was a member of. Bare repos
    //    under /var/lib/vigo/swarm/gitmirror/ are root-owned and
    //    survive — admin cleanup is a separate path.
    let gm_user_dir = home.join(".vigo-gitmirror");
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
/// cryptsync rewrap, and records the retirement entry. On subsequent
/// calls inside the same window, only a fresh claim code is issued —
/// the keypair and cryptsync state are not rotated again.
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
            // ADR-019: when a gitmirror project's delegation chain has
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
    let new_salt = crate::swarm::cryptsync::crypt::fresh_salt();
    let new_blob = wrap_signing_key(&new_key, passphrase.as_bytes(), &new_salt)
        .context("wrapping new puddle identity")?;

    // ADR-019 pre-flight: check every gitmirror project the user
    // founded against the delegation cap. Aborts here BEFORE any
    // keypair mutation when a project would push past the cap and
    // the operator hasn't passed --force-orphan-projects.
    let pre_flight = crate::swarm::gitmirror::rekey::pre_flight_check_caps(
        &home,
        &old_pubkey_text,
    )
    .context("gitmirror pre-flight cap check")?;
    if !pre_flight.at_cap.is_empty() && !force_orphan_projects {
        bail!(
            "{} gitmirror project(s) at delegation cap ({}/{}): {}\n  \
             Run `vigo swarm gitmirror project re-found <handle>` per project to \
             mint a fresh project_id, or pass --force-orphan-projects to proceed \
             and mark these projects frozen-at-rekey.",
            pre_flight.at_cap.len(),
            crate::swarm::gitmirror::envelopes::DELEGATION_MAX_LEN,
            crate::swarm::gitmirror::envelopes::DELEGATION_MAX_LEN,
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

    // Persist retirement entry before the cryptsync sync. If the
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

    // Cryptsync rewrap + TOFU pin clear + recipients regen. Logged
    // outcome — not fatal for the rotation if the user has no
    // cryptsync state.
    let outcome = rekey::apply_post_rotation_cryptsync_sync(
        &home,
        &username,
        &old_key,
        &new_key,
        &old_pubkey_text,
    )
    .context("cryptsync sync after rotation")?;

    // ADR-019: gitmirror rekey sync — sign + append delegations under
    // OLD for every project the user founded, rebuild tokens.json
    // (chain[0] bootstrap + fresh admin under NEW), rewrite member
    // entries OLD → NEW. Best-effort with per-project failure
    // tracking; the rotation itself has already committed.
    let gm_outcome =
        crate::swarm::gitmirror::rekey::apply_post_rotation_gitmirror_sync(
            &home,
            &old_key,
            &new_key,
            &old_pubkey_text,
            &new_pubkey_text,
            now,
            force_orphan_projects,
        )
        .context("gitmirror sync after rotation")?;

    // Restart the session helper with the new key so sibling
    // subsystems pick up the rotation immediately. Lock first
    // (idempotent), then spawn a fresh helper — same shape `unlock`
    // uses.
    let _ = session::request_lock();
    std::thread::sleep(std::time::Duration::from_millis(150));
    let parent_pid =
        resolve_shell_pid().unwrap_or_else(|| unsafe { libc::getppid() } as u32);
    if let Err(e) =
        session::spawn_helper(&new_key, parent_pid, DEFAULT_IDLE_MINUTES, &home)
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
    print_gitmirror_rekey_outcome(&gm_outcome);
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

fn print_gitmirror_rekey_outcome(
    outcome: &crate::swarm::gitmirror::rekey::GitmirrorRekeyOutcome,
) {
    if outcome.delegations_signed.is_empty()
        && outcome.members_updated.is_empty()
        && outcome.tokens_rebuilt.is_empty()
        && outcome.failures.is_empty()
        && outcome.in_warn_band.is_empty()
    {
        println!("  Gitmirror sync:   skipped (no projects under ~/.vigo-gitmirror)");
        return;
    }
    if !outcome.delegations_signed.is_empty() {
        println!(
            "  Gitmirror sync:   delegated {} project(s): {}",
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
            crate::swarm::gitmirror::envelopes::DELEGATION_WARN_LEN,
            outcome.in_warn_band.join(", ")
        );
        println!(
            "                       consider `vigo swarm gitmirror project re-found <handle>` for hygiene"
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

fn print_rewrap_outcome(outcome: crate::swarm::cryptsync::identity_rewrap::RewrapOutcome) {
    use crate::swarm::cryptsync::identity_rewrap::RewrapOutcome;
    match outcome {
        RewrapOutcome::NotEnrolled => {
            println!("  Cryptsync rewrap: skipped (not enrolled on this envoy)");
        }
        RewrapOutcome::Rewrapped { backup_path } => {
            println!(
                "  Cryptsync rewrap: ok (backup at {})",
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
///                                gitmirror project is at the delegation cap;
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
    // the cryptsync rewrap. The founder uses the same passphrase
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
    let pre_flight = crate::swarm::gitmirror::rekey::pre_flight_check_caps(
        &home,
        &old_pubkey_text,
    )
    .context("gitmirror pre-flight cap check")?;
    if !pre_flight.at_cap.is_empty() && !force_orphan_projects {
        bail!(
            "{} gitmirror project(s) at delegation cap ({}/{}): {}\n  \
             Run `vigo swarm gitmirror project re-found <handle>` per project to \
             mint a fresh project_id, or pass --force-orphan-projects to proceed \
             and mark these projects frozen-at-rekey.",
            pre_flight.at_cap.len(),
            crate::swarm::gitmirror::envelopes::DELEGATION_MAX_LEN,
            crate::swarm::gitmirror::envelopes::DELEGATION_MAX_LEN,
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

    let outcome = rekey::apply_post_rotation_cryptsync_sync(
        &home,
        &username,
        &old_key,
        &new_key,
        &old_pubkey_text,
    )
    .context("cryptsync sync after rotation")?;

    // ADR-019: gitmirror sync — same as the founder side. Each envoy
    // in the puddle signs its OWN delegation locally using its copy
    // of the OLD puddle key (which is the same shared keypair, so the
    // canonical signed bytes are identical modulo `delegated_at`)
    // and rebuilds tokens.json with the fresh admin under NEW. Each
    // envoy maintains a self-consistent local view.
    let gm_outcome =
        crate::swarm::gitmirror::rekey::apply_post_rotation_gitmirror_sync(
            &home,
            &old_key,
            &new_key,
            &old_pubkey_text,
            &new_pubkey_text,
            now,
            force_orphan_projects,
        )
        .context("gitmirror sync after rotation")?;

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
    print_gitmirror_rekey_outcome(&gm_outcome);
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
    // ADR-015 (gitmirror substrate) and ADR-016 (cryptsync membership
    // on puddle) shipped, so peer puddle pubkeys ARE observable —
    // but via the cryptsync peer table, not the puddle module's own
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
         this envoy has discovered via swarm announces), inspect `~/.cryptsync/recipients.txt`\n\
         or run `vigocli swarm cryptsync show <user>` from an admin shell for the server-\n\
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

/// If any peer in the local cryptsync peer table is announcing under
/// a puddle pubkey that doesn't match this envoy's local puddle pubkey,
/// this envoy was probably offline during a rekey on the founder.
/// Print a one-shot hint so the operator knows where to migrate from.
///
/// Comparison is on the bare hex (after stripping the `ed25519:`
/// prefix and lowercasing). cryptsync state stores puddle_pubkey
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
    let table = crate::swarm::cryptsync::state::PeerTable::load(username);
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
