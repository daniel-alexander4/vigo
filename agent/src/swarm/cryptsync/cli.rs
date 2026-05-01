// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! User-facing `vigo swarm cryptsync <verb>` subcommand tree.
//!
//! All verbs are **user-mode** operations — they touch files under
//! the invoking user's `~/.cryptsync/` and their `~/cryptsync/`
//! directory. They do not require root and do not contact the
//! server or the agent daemon for Week 1 verbs.
//!
//! ```text
//! vigo swarm cryptsync fleet init                    # under self-fleet membership (ADR-016)
//! vigo swarm cryptsync standalone init               # opt out of fleet mode (legacy gossip)
//! vigo swarm cryptsync standalone enroll             # per-envoy, one-time
//! vigo swarm cryptsync status                        # show local state
//! vigo swarm cryptsync unlock                        # (week 3) hold key in session helper
//! vigo swarm cryptsync lock                          # (week 3) scrub held key
//! vigo swarm cryptsync encrypt <file> [--as <name>]  # (week 3) add to ~/cryptsync/
//! vigo swarm cryptsync decrypt <file> -o <path>      # (week 3) decrypt to user path
//! vigo swarm cryptsync forget <hostname>             # (week 3) revoke an envoy
//! vigo swarm cryptsync reencrypt                     # (week 3) force re-encrypt
//! ```
//!
//! Phase 1 Week 1 implements `init`, `enroll`, and `status`. The
//! other verbs stub with a "not yet implemented" error until weeks 2
//! and 3 land.

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use age::x25519::Identity;
use anyhow::{anyhow, bail, Context, Result};
use zeroize::Zeroizing;

use super::config;
use super::crypt;
use super::session;
use super::state::PeerTable;
use super::tombstones;

/// Dispatch `vigo swarm cryptsync <verb> [args...]`.
pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "help" | "--help" | "-h" => {
            print_usage();
            Ok(())
        }
        // Mode-specific lifecycle subtrees (ADR-016). `fleet *` is the
        // path of least resistance once self-fleet is set up;
        // `standalone *` is the explicit opt-out for users who don't
        // want fleet membership wiring.
        "fleet" => dispatch_fleet(&args[1..]),
        "standalone" => dispatch_standalone(&args[1..]),
        // Mode-agnostic verbs — these don't care which mode is active.
        // Recipient resolution, session helper, encryption, decryption,
        // and lifecycle ops all read whatever local state exists and
        // do the right thing regardless of how it got there.
        "status" => cmd_status(&args[1..]),
        "unlock" => cmd_unlock(&args[1..]),
        "lock" => cmd_lock(&args[1..]),
        "encrypt" => cmd_encrypt(&args[1..]),
        "decrypt" => cmd_decrypt(&args[1..]),
        "delete" => cmd_delete(&args[1..]),
        "list" | "ls" => cmd_list(&args[1..]),
        "forget" => cmd_forget(&args[1..]),
        "leave" => cmd_leave(&args[1..]),
        "reencrypt" => cmd_reencrypt(&args[1..]),
        // Hidden verb — invoked by the server via TaskDispatch when an
        // admin runs `vigocli cryptsync evict`. Not advertised in --help.
        "_apply-evict" => cmd_apply_evict(&args[1..]),
        // Hidden verb — invoked by the server via TaskDispatch on
        // `swarm.cryptsync.blacklist` add. Host-level eviction across
        // every locally-enrolled user. Not advertised in --help.
        "_apply-blacklist" => cmd_apply_blacklist(&args[1..]),
        other => bail!(
            "unknown `vigo swarm cryptsync` subcommand: {}\nrun `vigo swarm cryptsync --help` for usage",
            other
        ),
    }
}

fn dispatch_fleet(args: &[String]) -> Result<()> {
    let sub = args.first().map(String::as_str).unwrap_or("");
    match sub {
        "" | "help" | "--help" | "-h" => {
            print_fleet_usage();
            Ok(())
        }
        "init" => cmd_fleet_init(&args[1..]),
        "renew-cert" => cmd_fleet_renew_cert(&args[1..]),
        other => bail!(
            "unknown `vigo swarm cryptsync fleet` subcommand: {}\n\
             run `vigo swarm cryptsync fleet --help` for usage",
            other
        ),
    }
}

fn dispatch_standalone(args: &[String]) -> Result<()> {
    let sub = args.first().map(String::as_str).unwrap_or("");
    match sub {
        "" | "help" | "--help" | "-h" => {
            print_standalone_usage();
            Ok(())
        }
        "init" => cmd_standalone_init(&args[1..]),
        "enroll" => cmd_standalone_enroll(&args[1..]),
        other => bail!(
            "unknown `vigo swarm cryptsync standalone` subcommand: {}\n\
             run `vigo swarm cryptsync standalone --help` for usage",
            other
        ),
    }
}

fn print_fleet_usage() {
    eprintln!(
        "vigo swarm cryptsync fleet — fleet-mode lifecycle (ADR-016)

USAGE:
    vigo swarm cryptsync fleet <SUBCOMMAND>

SUBCOMMANDS:
    init          First-time setup of this user's per-envoy age key under fleet
                  membership. Issues a fleet-membership certificate (ADR-016 follow-up)
                  and persists it at ~/.cryptsync/fleet_cert.json. Requires self-fleet
                  to be initialized AND unlocked for this user (the cert is signed by
                  the self-fleet session helper).
    renew-cert    Re-issue the fleet-membership certificate. Default lifetime 30 days;
                  certs expire and need renewal. Run from any envoy where self-fleet
                  is unlocked — the renewed cert ships in the next announce tick. Use
                  this after a self-fleet rotation, or when peers warn-log a cert
                  expiry against this envoy.

Daily verbs (unlock / encrypt / decrypt / status / leave / …) live at the top
level — they don't care which mode is active. See `vigo swarm cryptsync --help`."
    );
}

fn print_standalone_usage() {
    eprintln!(
        "vigo swarm cryptsync standalone — standalone-mode lifecycle (ADR-016)

USAGE:
    vigo swarm cryptsync standalone <SUBCOMMAND>

SUBCOMMANDS:
    init     First-time setup of this user's per-envoy age key without registering
             with a self-fleet. Recipients are discovered via pairwise gossip; this
             is the legacy path that pre-dates self-fleet.
    enroll   Per-envoy bootstrap when joining an existing standalone mesh (typically
             triggered by an `enroll.pending` sentinel left by the user executor).

Daily verbs (unlock / encrypt / decrypt / status / leave / …) live at the top
level — they don't care which mode is active. See `vigo swarm cryptsync --help`."
    );
}

fn print_usage() {
    eprintln!(
        "vigo swarm cryptsync — per-user encrypted P2P directory

USAGE:
    vigo swarm cryptsync <SUBCOMMAND>

DAILY (mode-agnostic):
    unlock                         Prompt password, start session helper
    lock                           Scrub held key and stop session helper
    encrypt <file> [--as <name>]   Encrypt a plaintext file into ~/cryptsync/
    decrypt <file> -o <path>       Decrypt from ~/cryptsync/ to a user-chosen path
    delete <name>                  Remove a file from ~/cryptsync/; tombstone propagates to peers
    list [--long] [--tombstones]   List ciphertext files + tombstones in ~/cryptsync/
    forget <hostname>              Revoke an envoy from the recipient set (local only)
    leave [--purge] [--keep-identity] [--yes]  Unenroll this user from cryptsync on this envoy
    reencrypt                      Force re-encrypt of ~/cryptsync/ to current recipients
    status                         Show local cryptsync state

LIFECYCLE (mode-specific subtrees, ADR-016):
    fleet init                     Set up per-envoy age key under self-fleet membership
                                   (usually auto-run by `self-fleet unlock` / `join`)
    standalone init                Set up per-envoy age key without fleet membership
                                   (legacy pairwise-gossip path)
    standalone enroll              Per-envoy bootstrap into an existing standalone mesh

All subcommands operate on the invoking user's home:
    ~/.cryptsync/    wrapped identity, salt, recipients, config
    ~/cryptsync/     ciphertext-at-rest content directory

No root required. Does not contact the Vigo server."
    );
}

// ---------- fleet init ----------
//
// Generates a per-envoy age identity wrapped under the cryptsync
// password and seeds recipients.txt. Functionally identical to
// `standalone init`; the difference is operator intent (and the
// help text). Once self-fleet is initialized for this user, the
// announce loop automatically scopes recipient resolution to the
// fleet — no flag is needed at this command level.
//
// Refuses if self-fleet has not been initialized for this user.
// Pointing the user at `vigo swarm self-fleet init` first is the
// correct path: cryptsync fleet mode is meaningless without a fleet
// identity to scope membership to.
fn cmd_fleet_init(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if !crate::swarm::self_fleet::is_initialized(&home) {
        bail!(
            "self-fleet is not initialized for this user; fleet-mode cryptsync requires \
             a fleet identity to scope recipient resolution.\n\
             Run `vigo swarm self-fleet init` first, then re-run this command. \
             (Or use `vigo swarm cryptsync standalone init` to opt out of fleet mode.)"
        );
    }
    bootstrap_identity(&home, &username, /* is_init */ true, config::WrapMode::Fleet)?;
    // Auto-issue the fleet membership certificate so receivers can
    // cryptographically verify our fleet claim from the very first
    // announce. Fails noisily but doesn't undo the bootstrap — the
    // user can re-run `cryptsync fleet renew-cert` separately.
    if let Err(e) = issue_fleet_cert(&home, &username) {
        eprintln!(
            "warning: cryptsync fleet identity is set up but the fleet-membership cert \
             could not be issued: {:#}\n\
             Re-run `vigo swarm cryptsync fleet renew-cert` once self-fleet is unlocked.",
            e
        );
    }
    Ok(())
}

fn cmd_fleet_renew_cert(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if !crate::swarm::self_fleet::is_initialized(&home) {
        bail!(
            "self-fleet is not initialized for this user; cannot issue fleet cert.\n\
             Run `vigo swarm self-fleet init` first."
        );
    }
    let cs_identity = crate::swarm::cryptsync::config::identity_path(&home);
    if !cs_identity.exists() {
        bail!(
            "cryptsync identity is missing at {} — run `vigo swarm cryptsync fleet init` first.",
            cs_identity.display()
        );
    }
    issue_fleet_cert(&home, &username)
}

/// Build, sign, and persist a fresh fleet-membership cert.
/// Reads:
///   * the local age pubkey from `~/.cryptsync/recipients.txt` (first line)
///   * the local self-fleet pubkey from `~/.vigo-fleet/identity.pub`
/// Signs via the self-fleet session helper (so the user's session
/// must be unlocked when this runs). On success, writes the cert
/// to `~/.cryptsync/fleet_cert.json` and chowns it to the user.
fn issue_fleet_cert(home: &Path, username: &str) -> Result<()> {
    use crate::swarm::cryptsync::fleet_cert::{FleetCert, DEFAULT_LIFETIME_SECS, SIG_LEN};

    let age_pubkey = read_local_age_pubkey(home)?;
    let fleet_pubkey = match crate::swarm::self_fleet::local_fleet_pubkey(home)? {
        Some(s) => s
            .strip_prefix("ed25519:")
            .map(|s| s.to_ascii_lowercase())
            .unwrap_or_else(|| s.to_ascii_lowercase()),
        None => bail!("self-fleet pubkey not readable; run `self-fleet init` first"),
    };
    let issued_at = now_secs();
    let cert = FleetCert::issue(
        &age_pubkey,
        &fleet_pubkey,
        issued_at,
        DEFAULT_LIFETIME_SECS,
        |bytes| {
            let sig = crate::swarm::self_fleet::session::request_sign(bytes).map_err(|e| {
                anyhow!(
                    "self-fleet session helper unavailable: {}\n\
                     run `vigo swarm self-fleet unlock` first",
                    e
                )
            })?;
            if sig.len() != SIG_LEN {
                bail!(
                    "session helper returned a {}-byte signature, expected {}",
                    sig.len(),
                    SIG_LEN
                );
            }
            let mut out = [0u8; SIG_LEN];
            out.copy_from_slice(&sig);
            Ok(out)
        },
    )?;
    cert.save(home)?;
    // chown the cert file to the user — bootstrap_identity uses
    // create_user_dir with the same posture; mirror it here.
    #[cfg(unix)]
    {
        let path = crate::swarm::cryptsync::fleet_cert::cert_path(home);
        let _ = crate::swarm::cryptsync::chown_to_user_pub(&path, username);
    }
    let _ = username; // keep parameter live on non-unix platforms
    println!(
        "✓ Issued fleet-membership cert (expires in {} days)",
        cert.days_until_expiry(issued_at)
    );
    println!(
        "  wrote {}",
        crate::swarm::cryptsync::fleet_cert::cert_path(home).display()
    );
    Ok(())
}

/// Pull the first non-comment line out of recipients.txt — that's
/// always this envoy's own age pubkey because `bootstrap_identity`
/// seeds it as the first entry.
fn read_local_age_pubkey(home: &Path) -> Result<String> {
    let path = crate::swarm::cryptsync::config::recipients_path(home);
    let body = std::fs::read_to_string(&path)
        .with_context(|| format!("read {}", path.display()))?;
    for line in body.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') {
            continue;
        }
        return Ok(trimmed.to_string());
    }
    bail!("no age pubkey found in {}", path.display())
}

// ---------- standalone init ----------

fn cmd_standalone_init(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    bootstrap_identity(&home, &username, /* is_init */ true, config::WrapMode::Standalone)
}

// ---------- standalone enroll ----------

fn cmd_standalone_enroll(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    bootstrap_identity(&home, &username, /* is_init */ false, config::WrapMode::Standalone)
}

/// Generate a fresh wrapped identity, seed `recipients.txt` with the
/// freshly-minted pubkey, and create `~/cryptsync/`. Shared between
/// `init` and `enroll` — the mechanics are identical, only the
/// operator-facing wording differs. `username` is the invoker
/// resolved by `user_for_invocation` and is used to chown every
/// file we drop under `~/.cryptsync/` and `~/cryptsync/` so the elevated
/// (sudo'd) process doesn't leave root-owned state in the user's
/// home.
fn bootstrap_identity(
    home: &Path,
    username: &str,
    is_init: bool,
    wrap_mode: config::WrapMode,
) -> Result<()> {
    let dot = config::dot_dir(home);
    create_user_dir(&dot, username, 0o700)
        .with_context(|| format!("creating {}", dot.display()))?;

    let id_path = config::identity_path(home);
    let salt_path = config::salt_path(home);
    let rec_path = config::recipients_path(home);
    let content_dir = config::content_dir(home);
    let marker = config::enroll_pending_path(home);

    if id_path.exists() {
        bail!(
            "{} already exists — this envoy is already enrolled.\n\
             To re-enroll, delete ~/.cryptsync/ first (destroys decryption capability).",
            id_path.display()
        );
    }

    // Source the wrap-key material per ADR-016. Standalone prompts
    // for a per-envoy passphrase; fleet derives from the self-fleet
    // identity via the running session helper. Either way, we end up
    // with a byte string we can hand to `wrap_identity` as the
    // `password` argument — Argon2id stretches it into the AEAD key
    // identically in both modes, so the on-disk envelope shape stays
    // version-stable.
    let wrap_password: Zeroizing<Vec<u8>> = match wrap_mode {
        config::WrapMode::Standalone => {
            let password = prompt_password(is_init)?;
            if password.is_empty() {
                bail!("password must not be empty");
            }
            Zeroizing::new(password.into_bytes())
        }
        config::WrapMode::Fleet => {
            // Fleet mode: passphrase is derived from the self-fleet
            // identity. The self-fleet session helper must be running.
            // No prompt — that's the whole point of fleet mode.
            crypt::derive_fleet_wrap_password().context(
                "fleet-mode wrap-key derivation failed; ensure self-fleet is unlocked"
            )?
        }
    };

    // Generate identity + salt + wrap.
    //
    // Note: the Ed25519 signing identity for this user is generated
    // lazily by the cryptsync daemon (`ensure_signing_identity`), not
    // here. The signing key authorizes DELETE envelopes and lives in
    // root-only `/var/lib/vigo/swarm/cryptsync/<user>/signing.key` so
    // deletions can propagate autonomously on the daemon's schedule
    // without requiring the user to be unlocked. A compromised signing
    // key is rapidly recoverable via `vigo swarm cryptsync forget` on peers;
    // a compromised encryption key is not, which is why only the
    // encryption identity is password-wrapped.
    let identity = Identity::generate();
    let public = identity.to_public();
    let salt = crypt::fresh_salt();
    let blob = crypt::wrap_identity(&identity, wrap_password.as_slice(), &salt)
        .context("wrapping identity")?;

    // Write wrapped identity + salt (0600, owned by the user).
    write_private(&id_path, &blob, username)?;
    write_private(&salt_path, &salt, username)?;

    // Seed recipients.txt with this envoy's pubkey.
    append_recipient(&rec_path, &public.to_string(), username)?;

    // Create ~/cryptsync/ (0700, owned by the user).
    create_user_dir(&content_dir, username, 0o700)?;

    // Seed a self-documenting config.yaml if one doesn't already exist so
    // the operator opening ~/.cryptsync/config.yaml can tell what it's for
    // without consulting external docs. Skip if present (don't clobber
    // operator edits).
    let cfg_path = config::config_path(home);
    if !cfg_path.exists() {
        if let Err(e) = write_config_template(&cfg_path, username, wrap_mode) {
            tracing::warn!(
                path = %cfg_path.display(),
                error = %e,
                "cryptsync: failed to seed config.yaml template (not fatal)"
            );
        }
    } else {
        // Config exists — likely from a stale partial enroll or
        // operator pre-edit. Atomically rewrite just the wrap_mode
        // line so daily `unlock` routes correctly. Other fields are
        // left intact via `save_config` over a freshly-loaded view.
        let mut existing = config::load_config(home);
        existing.wrap_mode = wrap_mode;
        if let Err(e) = config::save_config(home, &existing, username) {
            tracing::warn!(
                path = %cfg_path.display(),
                error = %e,
                "cryptsync: failed to record wrap_mode in existing config.yaml"
            );
        }
    }

    // Remove the enroll.pending sentinel if present.
    if marker.exists() {
        if let Err(e) = fs::remove_file(&marker) {
            tracing::warn!(
                marker = %marker.display(),
                error = %e,
                "cryptsync: failed to remove enroll.pending sentinel after enrollment"
            );
        }
    }

    // Drop regen.pending so the daemon reactor folds existing live
    // peers into the freshly-seeded recipients.txt on its next tick.
    // Without this, recipients.txt would stay at "self only" until
    // a peer rotates its pubkey or ages out — heartbeat announces
    // from already-known peers don't trigger a regen.
    let regen_marker = config::regen_pending_path(home);
    if let Err(e) = write_user_file(&regen_marker, b"", username) {
        tracing::warn!(
            path = %regen_marker.display(),
            error = %e,
            "cryptsync: failed to drop regen.pending sentinel; recipients sync delayed until next peer change"
        );
    }

    println!("wrote {} (0600, wrapped)", id_path.display());
    println!("wrote {} (0600)", salt_path.display());
    println!("wrote {}", rec_path.display());
    println!("created {} (0700)", content_dir.display());
    if cfg_path.exists() {
        println!("wrote {} (template with inline docs)", cfg_path.display());
    }
    println!();
    println!("public key: {}", public);
    println!();
    if is_init {
        println!("This envoy is now the primary. Add more envoys with `vigo swarm cryptsync standalone enroll` (or `fleet init` on each envoy if you use self-fleet)");
        println!("after provisioning them via the user's usercrate with `cryptsync: true`.");
    } else {
        println!("This envoy is now enrolled. The primary envoy's reactor will pick up");
        println!("this pubkey on the next announcement cycle and re-encrypt existing");
        println!("files to include it (coming in Phase 1 Week 2).");
    }
    Ok(())
}

// ---------- status ----------

fn cmd_status(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let dot = config::dot_dir(&home);
    let content = config::content_dir(&home);

    println!("cryptsync status");
    println!("  home:             {}", home.display());
    println!("  dot_dir:          {}", dot.display());
    println!("  content_dir:      {}", content.display());
    println!("  enrolled:         {}", config::is_enrolled(&home));
    println!("  enroll pending:   {}", config::has_pending_enroll(&home));

    if config::is_enrolled(&home) {
        match fs::read_to_string(config::recipients_path(&home)) {
            Ok(text) => {
                let count = text
                    .lines()
                    .map(str::trim)
                    .filter(|l| !l.is_empty() && !l.starts_with('#'))
                    .count();
                println!("  recipients:       {}", count);
            }
            Err(_) => println!("  recipients:       (recipients.txt missing)"),
        }
    }

    if content.is_dir() {
        let counts = scan_content_subcounts(&content);
        println!("  files:            {}", counts.total());
        println!("    ciphertext:     {}", counts.ciphertext);
        if counts.plaintext > 0 {
            println!(
                "    plaintext:      {}   (run 'vigo swarm cryptsync unlock' to encrypt and sync)",
                counts.plaintext
            );
        } else {
            println!("    plaintext:      0");
        }
        if counts.failed.is_empty() {
            println!("    failed:         0");
        } else {
            println!("    failed:         {}", counts.failed.len());
            for name in &counts.failed {
                println!("                    {}", name);
            }
        }
    } else {
        println!("  files:            (content dir missing)");
    }

    // Tombstone count. The canonical table lives in
    // /var/lib/vigo/swarm/cryptsync/<user>/tombstones.json (root 0600), but the
    // daemon mirrors every save to ~/.cryptsync/tombstones.json (user
    // 0600). Read the user-side mirror so this works without sudo.
    if config::is_enrolled(&home) {
        let username = home
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("");
        if !username.is_empty() {
            let table = tombstones::TombstoneTable::load_user(username, &home);
            println!("  tombstones:       {}", table.tombstones.len());
        }
    }

    let cfg = config::load_config(&home);
    println!("  cross_subnet_peers:      {}", cfg.cross_subnet_peers.len());
    println!("  idle_timeout:     {} min", cfg.idle_timeout_minutes);

    // ADR-016 follow-up: fleet membership cert state. Surface this
    // when self-fleet is initialized for the user (otherwise it's
    // not relevant — they're running standalone).
    if crate::swarm::self_fleet::is_initialized(&home) {
        match crate::swarm::cryptsync::fleet_cert::FleetCert::load(&home) {
            None => println!(
                "  fleet cert:       (not issued — run `vigo swarm cryptsync fleet renew-cert`)"
            ),
            Some(cert) => {
                let now = now_secs();
                let days = cert.days_until_expiry(now);
                if days < 0 {
                    println!(
                        "  fleet cert:       EXPIRED {} day(s) ago — run `vigo swarm cryptsync fleet renew-cert`",
                        -days
                    );
                } else if days < 7 {
                    println!(
                        "  fleet cert:       expires in {} day(s) — consider running `vigo swarm cryptsync fleet renew-cert`",
                        days
                    );
                } else {
                    println!("  fleet cert:       valid (expires in {} day(s))", days);
                }
            }
        }
    }

    // Session helper state.
    match session::request_status() {
        Ok(msg) => println!("  session helper:   {}", msg),
        Err(_) => println!("  session helper:   not running (locked)"),
    }
    Ok(())
}

// ---------- unlock ----------

fn cmd_unlock(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo swarm cryptsync fleet init` if you \
             use self-fleet, or `vigo swarm cryptsync standalone init` to opt out of \
             fleet mode (run `vigo swarm cryptsync --help` for the full setup tree)"
        );
    }
    let id_path = config::identity_path(&home);
    let salt_path = config::salt_path(&home);
    let blob = fs::read(&id_path)
        .with_context(|| format!("reading {}", id_path.display()))?;
    let salt = fs::read(&salt_path)
        .with_context(|| format!("reading {}", salt_path.display()))?;

    // Route the wrap-key source by the mode persisted at init time.
    // Standalone: prompt the operator. Fleet: ask the self-fleet
    // session helper (no prompt). Failure to source the key in either
    // case is fatal — we don't fall back across modes since the wrong
    // mode would just produce an AEAD tag mismatch downstream and a
    // less useful error.
    let cfg = config::load_config(&home);
    let wrap_password: Zeroizing<Vec<u8>> = match cfg.wrap_mode {
        config::WrapMode::Standalone => {
            let password = prompt_password(false)?;
            if password.is_empty() {
                bail!("password must not be empty");
            }
            Zeroizing::new(password.into_bytes())
        }
        config::WrapMode::Fleet => crypt::derive_fleet_wrap_password().context(
            "fleet-mode unlock requires the self-fleet session helper to be running; \
             run `vigo swarm self-fleet unlock` first",
        )?,
    };
    let identity = crypt::unwrap_identity(&blob, wrap_password.as_slice(), &salt)?;

    // Parent shell PID — so the session helper can self-terminate
    // when the shell exits. Naive `getppid()` returns the immediate
    // parent, which is `sudo`, `env`, or another wrapper when the
    // user invokes `sudo env HOME=… vigo swarm cryptsync unlock` (the only
    // pattern that actually works against env-stripping sudo
    // configs). Those wrappers exit a moment after `vigo unlock`
    // returns, so the helper's watchdog would see a dead PID and
    // shut the helper down within one ~10s tick — making
    // unlock-then-decrypt-in-a-separate-command fail with
    // "session helper not running" 100% of the time. Walk up the
    // ppid chain past known intermediates to find the actual shell.
    let parent_pid = resolve_shell_pid()
        .unwrap_or_else(|| unsafe { libc::getppid() } as u32);
    session::spawn_helper(&identity, parent_pid, cfg.idle_timeout_minutes, &home)?;

    println!("✓ Unlocked. Session helper running until logout, lock, or {} min idle.",
        cfg.idle_timeout_minutes);

    // Drain any plaintext that landed in ~/cryptsync/ while locked
    // (ADR-012 unlock-sweep). Best-effort: each per-file failure is
    // surfaced inline; a partial sweep doesn't fail the unlock.
    let username = user_for_invocation()?;
    super::sweep_plaintext_on_unlock(&home, &username);
    Ok(())
}

// ---------- lock ----------

fn cmd_lock(_args: &[String]) -> Result<()> {
    session::request_lock()?;
    println!("✓ Locked. Key scrubbed.");
    Ok(())
}

// ---------- encrypt ----------

fn cmd_encrypt(args: &[String]) -> Result<()> {
    let mut file: Option<PathBuf> = None;
    let mut as_name: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--as" => {
                i += 1;
                as_name = Some(
                    args.get(i)
                        .context("--as requires a filename")?
                        .clone(),
                );
            }
            s if s.starts_with("--") => bail!("unknown flag: {}", s),
            s => {
                if file.is_some() {
                    bail!("only one source file accepted");
                }
                file = Some(PathBuf::from(s));
            }
        }
        i += 1;
    }
    let src = file.context("usage: vigo swarm cryptsync encrypt <file> [--as <name>]")?;
    let src_meta = fs::metadata(&src)
        .with_context(|| format!("stat {}", src.display()))?;
    if !src_meta.is_file() {
        bail!("{} is not a regular file", src.display());
    }
    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo swarm cryptsync fleet init` if you \
             use self-fleet, or `vigo swarm cryptsync standalone init` to opt out of \
             fleet mode (run `vigo swarm cryptsync --help` for the full setup tree)"
        );
    }
    let recipients = config::load_recipients(&home)?;
    let target_name = match as_name {
        Some(n) => n,
        None => src
            .file_name()
            .and_then(|n| n.to_str())
            .context("source has no basename")?
            .to_string(),
    };
    if !valid_content_name(&target_name) {
        bail!(
            "invalid target name `{}` — no path separators, no dotfiles, no nulls",
            target_name
        );
    }
    let content_dir = config::content_dir(&home);
    if !content_dir.is_dir() {
        bail!(
            "{} does not exist — run `vigo swarm cryptsync standalone enroll` (or `fleet init` if you use self-fleet) first",
            content_dir.display()
        );
    }
    let dst = content_dir.join(&target_name);
    let username = user_for_invocation()?;

    // Atomic write via temp file, so a crash mid-encrypt doesn't
    // leave a half-written ciphertext in ~/cryptsync/.
    let tmp = content_dir.join(format!(".{}.cryptsync.enc.tmp", target_name));
    if let Err(e) = crypt::encrypt_file(&src, &tmp, &recipients) {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }
    if let Err(e) = super::finalize_user_ciphertext(&tmp, &dst, &username) {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }

    println!(
        "✓ Encrypted to {} recipient(s); wrote {}",
        recipients.len(),
        dst.display()
    );
    Ok(())
}

// ---------- decrypt ----------

fn cmd_decrypt(args: &[String]) -> Result<()> {
    let mut name: Option<String> = None;
    let mut out_path: Option<PathBuf> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "-o" | "--output" => {
                i += 1;
                out_path = Some(PathBuf::from(
                    args.get(i).context("-o requires a path")?,
                ));
            }
            s if s.starts_with('-') => bail!("unknown flag: {}", s),
            s => {
                if name.is_some() {
                    bail!("only one source file accepted");
                }
                name = Some(s.to_string());
            }
        }
        i += 1;
    }
    let name = name.context("usage: vigo swarm cryptsync decrypt <file> -o <path>")?;
    let out = out_path.context("-o <path> is required")?;

    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo swarm cryptsync fleet init` if you \
             use self-fleet, or `vigo swarm cryptsync standalone init` to opt out of \
             fleet mode (run `vigo swarm cryptsync --help` for the full setup tree)"
        );
    }
    let content_dir = config::content_dir(&home);
    let src = content_dir.join(&name);
    if !src.is_file() {
        bail!("{} does not exist in {}", name, content_dir.display());
    }
    let ciphertext = fs::read(&src)
        .with_context(|| format!("reading {}", src.display()))?;

    // Talk to the session helper. `connect()` yields the right error
    // message when the helper isn't running.
    if session::connect().is_err() {
        bail!(
            "session helper not running — run `vigo swarm cryptsync unlock` first"
        );
    }
    let plaintext = session::request_decrypt(&ciphertext)?;

    // Write plaintext to -o path. If the destination file exists,
    // overwrite (user asked for it). Permissions: 0600 so a
    // default-umask shell doesn't accidentally world-read plaintext;
    // ownership chowned to the invoking user (resolved via SUDO_USER
    // → getuid passwd lookup) so the user can read their own
    // decrypted output even when the agent binary is invoked through
    // sudo and runs in a root context.
    let username = user_for_invocation()?;
    write_user_file(&out, &plaintext, &username)?;
    println!("✓ Wrote {} bytes to {}", plaintext.len(), out.display());
    Ok(())
}

// ---------- delete ----------

fn cmd_delete(args: &[String]) -> Result<()> {
    let name = args
        .first()
        .context("usage: vigo swarm cryptsync delete <name>")?
        .clone();
    if !valid_content_name(&name) {
        bail!(
            "invalid name `{}` — no path separators, no dotfiles, no nulls",
            name
        );
    }
    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo swarm cryptsync fleet init` if you \
             use self-fleet, or `vigo swarm cryptsync standalone init` to opt out of \
             fleet mode (run `vigo swarm cryptsync --help` for the full setup tree)"
        );
    }
    let content_dir = config::content_dir(&home);
    let target = content_dir.join(&name);
    if !target.exists() {
        bail!(
            "{} not found in {}",
            name,
            content_dir.display()
        );
    }
    fs::remove_file(&target)
        .with_context(|| format!("removing {}", target.display()))?;
    println!(
        "✓ Removed {}; tombstone will propagate on next watcher cycle.",
        target.display()
    );
    Ok(())
}

// ---------- list ----------

fn cmd_list(args: &[String]) -> Result<()> {
    let mut long = false;
    let mut tombstones_only = false;
    let mut sort_by_name = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--long" | "-l" => long = true,
            "--tombstones" | "--tombstones-only" => tombstones_only = true,
            "--sort=name" => sort_by_name = true,
            "--sort=mtime" => sort_by_name = false,
            s if s.starts_with("--") => bail!("unknown flag: {}", s),
            s => bail!("unexpected argument: {}", s),
        }
        i += 1;
    }

    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo swarm cryptsync fleet init` if you \
             use self-fleet, or `vigo swarm cryptsync standalone init` to opt out of \
             fleet mode (run `vigo swarm cryptsync --help` for the full setup tree)"
        );
    }
    let content_dir = config::content_dir(&home);

    // Load tombstone table from the user-side mirror at
    // ~/.cryptsync/tombstones.json (the daemon writes this on every save
    // alongside the canonical root-owned copy). Missing or malformed
    // mirror is treated as empty.
    let username = home
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or_default()
        .to_string();
    let tombstone_table = if username.is_empty() {
        tombstones::TombstoneTable::empty("")
    } else {
        tombstones::TombstoneTable::load_user(&username, &home)
    };

    // Build the row set.
    let mut rows: Vec<ListRow> = Vec::new();
    if !tombstones_only {
        if content_dir.is_dir() {
            for entry in fs::read_dir(&content_dir)
                .with_context(|| format!("reading {}", content_dir.display()))?
            {
                let entry = entry?;
                let path = entry.path();
                let name = match path.file_name().and_then(|n| n.to_str()) {
                    Some(n) => n.to_string(),
                    None => continue,
                };
                if name.starts_with('.') || !path.is_file() {
                    continue;
                }
                let meta = match entry.metadata() {
                    Ok(m) => m,
                    Err(_) => continue,
                };
                let mtime = meta
                    .modified()
                    .ok()
                    .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
                    .map(|d| d.as_secs() as i64)
                    .unwrap_or(0);
                rows.push(ListRow {
                    name: name.clone(),
                    size: Some(meta.len()),
                    mtime,
                    status: RowStatus::Live,
                    sha256: None,
                });
            }
        }
    }

    // Add tombstoned entries. Skip any filename already present as a
    // live file — would happen only during a (brief) race between
    // delete-fan-out and local removal, but handle cleanly.
    let now = now_secs();
    for (name, t) in &tombstone_table.tombstones {
        if rows.iter().any(|r| &r.name == name) {
            continue;
        }
        let retention_remaining_secs =
            tombstones::DEFAULT_RETENTION_SECS - (now - t.deletion_time);
        let days_left = (retention_remaining_secs / (24 * 60 * 60)).max(0);
        let status = RowStatus::Tombstoned { days_left };
        rows.push(ListRow {
            name: name.clone(),
            size: None,
            mtime: t.deletion_time,
            status,
            sha256: None,
        });
    }

    // Optional: populate sha256 for --long. Computed lazily so the
    // common case (no flag) doesn't pay the cost.
    if long {
        for row in rows.iter_mut() {
            if row.size.is_none() {
                continue;
            }
            let p = content_dir.join(&row.name);
            if let Ok(bytes) = std::fs::read(&p) {
                use sha2::Digest;
                let mut h = sha2::Sha256::new();
                h.update(&bytes);
                row.sha256 = Some(hex::encode(h.finalize()));
            }
        }
    }

    // Sort.
    if sort_by_name {
        rows.sort_by(|a, b| a.name.cmp(&b.name));
    } else {
        rows.sort_by(|a, b| b.mtime.cmp(&a.mtime));
    }

    if rows.is_empty() {
        if tombstones_only {
            println!("(no tombstones)");
        } else {
            println!("(no files in {})", content_dir.display());
        }
        return Ok(());
    }

    // Column widths sized to content, with minimum widths so short
    // rows don't produce a jagged header.
    let name_w = rows.iter().map(|r| r.name.len()).max().unwrap_or(0).max(8);
    let size_w = 10;
    let age_w = 18;
    let status_w = 24;

    println!(
        "  {:<name_w$}  {:>size_w$}  {:<age_w$}  {:<status_w$}{}",
        "FILENAME",
        "SIZE",
        "MODIFIED",
        "STATUS",
        if long { "  SHA256" } else { "" },
        name_w = name_w,
        size_w = size_w,
        age_w = age_w,
        status_w = status_w,
    );
    for row in &rows {
        let size_str = match row.size {
            Some(n) => format_size(n),
            None => "—".to_string(),
        };
        let age_str = format_age(now - row.mtime);
        let status_str = row.status.display();
        let sha_col = match (long, &row.sha256) {
            (true, Some(s)) => format!("  {}", &s[..16.min(s.len())]),
            (true, None) => "  —".to_string(),
            _ => String::new(),
        };
        println!(
            "  {:<name_w$}  {:>size_w$}  {:<age_w$}  {:<status_w$}{}",
            row.name,
            size_str,
            age_str,
            status_str,
            sha_col,
            name_w = name_w,
            size_w = size_w,
            age_w = age_w,
            status_w = status_w,
        );
    }
    Ok(())
}

struct ListRow {
    name: String,
    /// None for tombstoned rows (file is gone).
    size: Option<u64>,
    /// For live rows: file mtime. For tombstoned rows: deletion_time.
    mtime: i64,
    status: RowStatus,
    sha256: Option<String>,
}

#[derive(Clone, Copy)]
enum RowStatus {
    Live,
    Tombstoned { days_left: i64 },
}

impl RowStatus {
    fn display(&self) -> String {
        match self {
            RowStatus::Live => "live".to_string(),
            RowStatus::Tombstoned { days_left } if *days_left <= 0 => {
                "tombstoned (expiring)".to_string()
            }
            RowStatus::Tombstoned { days_left } => format!("tombstoned ({}d left)", days_left),
        }
    }
}

fn format_size(bytes: u64) -> String {
    const KIB: f64 = 1024.0;
    const MIB: f64 = 1024.0 * 1024.0;
    const GIB: f64 = 1024.0 * 1024.0 * 1024.0;
    let b = bytes as f64;
    if b >= GIB {
        format!("{:.1} GiB", b / GIB)
    } else if b >= MIB {
        format!("{:.1} MiB", b / MIB)
    } else if b >= KIB {
        format!("{:.1} KiB", b / KIB)
    } else {
        format!("{} B", bytes)
    }
}

fn format_age(seconds_ago: i64) -> String {
    if seconds_ago < 0 {
        return "(future)".to_string();
    }
    if seconds_ago < 60 {
        return format!("{}s ago", seconds_ago);
    }
    if seconds_ago < 3600 {
        return format!("{}m ago", seconds_ago / 60);
    }
    if seconds_ago < 86400 {
        return format!("{}h ago", seconds_ago / 3600);
    }
    let days = seconds_ago / 86400;
    if days < 365 {
        format!("{}d ago", days)
    } else {
        format!("{}y ago", days / 365)
    }
}

fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

// ---------- forget ----------

fn cmd_forget(args: &[String]) -> Result<()> {
    let hostname = args
        .first()
        .context("usage: vigo swarm cryptsync forget <hostname>")?
        .clone();
    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!("not enrolled on this envoy");
    }
    // Resolve the username from the home dir (last path segment).
    let username = home
        .file_name()
        .and_then(|n| n.to_str())
        .context("cannot resolve username from home dir")?
        .to_string();

    let mut table = PeerTable::load(&username);
    if !table.forget(&hostname) {
        bail!("{} is not in the peer table for {}", hostname, username);
    }
    table.save()?;

    // Regenerate recipients.txt immediately so new encrypts exclude
    // the revoked pubkey on the very next call.
    crate::swarm::cryptsync::regenerate_recipients_now(&username, &home)?;

    println!("✓ Forgot {}. Recipients regenerated.", hostname);
    println!("  Run `vigo swarm cryptsync reencrypt` to re-encrypt existing files");
    println!("  so the revoked host cannot decrypt them from its cached ciphertext.");
    Ok(())
}

// ---------- leave ----------
//
// Self-unenrollment. Three-stage handoff:
//
// 1. CLI (user context) validates state, reads own pubkey from
//    recipients.txt, writes `~/.cryptsync/leave.pending` with the leave
//    parameters, exits.
// 2. Daemon reactor (running as root) polls the sentinel every
//    announce cycle. When present: loads the daemon signing key,
//    builds + fans out the signed LEAVE envelope to every live peer,
//    records the leave in `/var/lib/vigo/swarm/cryptsync/<user>/leaves.json`,
//    scrubs user-side state per the sentinel's flags, removes the
//    sentinel.
// 3. Peers receiving the envelope via `POST /cryptsync/leaves/<user>`
//    verify the signature, apply locally (drop pubkey + regenerate
//    recipients + drop reencrypt.pending), persist.
//
// Offline peers miss the live fan-out but naturally drop the leaving
// envoy via STALE_SECS (90s without announcement); once this envoy
// stops announcing for user X, peers prune. Admin evict covers the
// "gone host with cached ciphertext" case via `vigocli cryptsync evict`.

#[derive(Debug, Default, serde::Serialize, serde::Deserialize)]
pub struct LeavePendingSpec {
    pub age_pubkey: String,
    pub hostname: String,
    pub leave_time: i64,
    pub keep_identity: bool,
    pub purge_content: bool,
}

fn cmd_leave(args: &[String]) -> Result<()> {
    let mut keep_identity = false;
    let mut purge_content = false;
    let mut yes = false;
    for a in args {
        match a.as_str() {
            "--keep-identity" => keep_identity = true,
            "--purge" => purge_content = true,
            "--yes" | "-y" => yes = true,
            "--help" | "-h" => {
                eprintln!(
                    "vigo swarm cryptsync leave — unenroll this user's cryptsync on this envoy\n\n\
                     USAGE:\n    vigo swarm cryptsync leave [--purge] [--keep-identity] [--yes]\n\n\
                     FLAGS:\n    \
                     --purge          also delete ~/cryptsync/ ciphertext (default: keep, orphaned)\n    \
                     --keep-identity  keep ~/.cryptsync/ so a re-enroll can reuse the wrapped\n                     \
                     identity. Default is to scrub.\n    \
                     --yes            skip the confirmation prompt"
                );
                return Ok(());
            }
            other if other.starts_with("--") => bail!("unknown flag: {}", other),
            _ => bail!("vigo swarm cryptsync leave takes no positional arguments; got {}", a),
        }
    }

    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!("not enrolled on this envoy — nothing to leave");
    }

    // Read own pubkey (user-readable — first non-comment line in
    // recipients.txt, written by init/enroll).
    let pubkey = read_local_pubkey(&home)?;
    let hostname = read_local_hostname()?;
    let leave_time = now_secs_signed();

    println!("This will unenroll this envoy from your cryptsync circle.");
    println!("  pubkey:   {}", pubkey);
    println!("  hostname: {}", hostname);
    if purge_content {
        println!("  ~/cryptsync/ ciphertext will be DELETED (--purge).");
    } else {
        println!("  ~/cryptsync/ ciphertext will be kept (orphaned — you can't decrypt here).");
    }
    if keep_identity {
        println!("  ~/.cryptsync/ wrapped identity will be KEPT (--keep-identity).");
    } else {
        println!("  ~/.cryptsync/ wrapped identity will be scrubbed.");
    }

    if !yes {
        use std::io::{self, BufRead, Write};
        print!("Proceed? [y/N] ");
        io::stdout().flush().ok();
        let stdin = io::stdin();
        let mut line = String::new();
        stdin.lock().read_line(&mut line)?;
        if !matches!(line.trim().to_ascii_lowercase().as_str(), "y" | "yes") {
            println!("aborted.");
            return Ok(());
        }
    }

    let spec = LeavePendingSpec {
        age_pubkey: pubkey,
        hostname,
        leave_time,
        keep_identity,
        purge_content,
    };
    let pending_path = config::leave_pending_path(&home);
    let json = serde_json::to_string_pretty(&spec).context("serializing leave spec")?;
    let username = user_for_invocation()?;
    write_user_file(&pending_path, json.as_bytes(), &username)?;

    println!();
    println!("\u{2713} leave queued at {}", pending_path.display());
    println!("  the agent will sign + propagate the LEAVE within one announce cycle (~30 s).");
    println!("  Run `vigo swarm cryptsync status` afterwards to confirm the sentinel cleared.");
    Ok(())
}

fn read_local_pubkey(home: &Path) -> Result<String> {
    let path = config::recipients_path(home);
    let text = fs::read_to_string(&path)
        .with_context(|| format!("reading {}", path.display()))?;
    for line in text.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') {
            continue;
        }
        return Ok(trimmed.to_string());
    }
    bail!("no pubkey found in {}", path.display())
}

fn read_local_hostname() -> Result<String> {
    let raw = fs::read_to_string("/etc/hostname")
        .context("reading /etc/hostname")?;
    let trimmed = raw.trim().to_ascii_lowercase();
    if trimmed.is_empty() {
        bail!("/etc/hostname is empty");
    }
    Ok(trimmed)
}

fn now_secs_signed() -> i64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

// ---------- _apply-evict (hidden, server-dispatched) ----------

/// Server-invoked hidden verb. Runs as root via TaskDispatch when an
/// admin runs `vigocli cryptsync evict`. Takes `--user <u>` + `--hostname
/// <h>`, looks the pubkey up from the local peer table (so the
/// admin dispatcher doesn't need to know it), and applies the
/// eviction via `apply_leave`. `--pubkey <p>` is optional: supply it
/// to bypass the peer-table lookup (e.g., when evicting a host that
/// never reached this envoy's multicast range).
///
/// Does NOT sign or fan out an envelope — admin path authority is the
/// mTLS control plane, not a P2P signature. No-op if the user is not
/// enrolled on this envoy OR the hostname isn't in this envoy's peer
/// table for that user.
fn cmd_apply_evict(args: &[String]) -> Result<()> {
    let mut user: Option<String> = None;
    let mut pubkey: Option<String> = None;
    let mut hostname: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--user" => {
                i += 1;
                user = Some(args.get(i).context("--user requires a value")?.clone());
                i += 1;
            }
            "--pubkey" => {
                i += 1;
                pubkey = Some(args.get(i).context("--pubkey requires a value")?.clone());
                i += 1;
            }
            "--hostname" => {
                i += 1;
                hostname = Some(args.get(i).context("--hostname requires a value")?.clone());
                i += 1;
            }
            other => bail!("unknown flag: {}", other),
        }
    }
    let user = user.context("--user is required")?;
    let hostname = hostname.context("--hostname is required")?;

    // Self-evict path: the server dispatches `_apply-evict` to *every*
    // envoy via `target: "*"`, including the evicted host itself. The
    // evicted host can't resolve the pubkey via peer-table lookup
    // (peer tables never include self) and used to early-return as a
    // no-op, leaving identity.age intact and blocking subsequent
    // re-enrollment. Detect the self case and scrub local cryptsync state
    // so the operator can re-enroll the host cleanly afterward.
    let local_hostname = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_default()
        .trim()
        .to_string();
    if !local_hostname.is_empty() && local_hostname == hostname {
        let home = resolve_user_home(&user)?;
        let dot = config::dot_dir(&home);
        if !config::is_enrolled(&home) {
            println!(
                "(no-op) {} is the local host but {} is not enrolled in cryptsync here",
                hostname, user
            );
            return Ok(());
        }
        // Mirror the !keep_identity scrub in process_one_leave —
        // remove the identity, salt, recipients, config; preserve
        // ~/cryptsync/ ciphertext (operator can `rm -rf ~/cryptsync` manually
        // if they want full purge).
        for name in ["identity.age", "identity.salt", "recipients.txt", "config.yaml"] {
            let p = dot.join(name);
            if p.exists() {
                if let Err(e) = std::fs::remove_file(&p) {
                    eprintln!("warning: failed to scrub {}: {}", p.display(), e);
                }
            }
        }
        println!(
            "\u{2713} self-evict on {}: scrubbed cryptsync identity for user {}; \
             re-enroll with `vigo swarm cryptsync standalone enroll` (or `fleet init` if you use self-fleet) to rejoin",
            hostname, user
        );
        return Ok(());
    }

    // Foreign-evict path: resolve the pubkey from the local peer
    // table (or use the caller-supplied one) and apply the LEAVE.
    let pubkey = match pubkey {
        Some(p) if p != "LOOKUP" && !p.is_empty() => p,
        _ => {
            let table = PeerTable::load(&user);
            match table.peers.get(&hostname) {
                Some(p) => p.pubkey.clone(),
                None => {
                    // No-op: hostname unknown to this envoy for this user.
                    // This is expected on envoys where the evicted host never
                    // appeared. Report success so task-dispatch doesn't retry.
                    println!(
                        "(no-op) {} is not in peers.json for user {} on this envoy",
                        hostname, user
                    );
                    return Ok(());
                }
            }
        }
    };

    // Resolve the user's home dir for recipients.txt regeneration.
    let home = resolve_user_home(&user)?;
    let leave_time = now_secs_signed();
    crate::swarm::cryptsync::apply_leave(&user, &pubkey, &hostname, leave_time, &home)
        .context("apply_leave")?;
    let shown = &pubkey[..pubkey.len().min(12)];
    println!(
        "\u{2713} evicted {} (pubkey {}...) from {}'s cryptsync circle on this envoy",
        hostname, shown, user
    );
    Ok(())
}

// ---------- _apply-blacklist (hidden, server-dispatched) ----------

/// Server-invoked hidden verb. Runs as root via TaskDispatch when an
/// admin adds a hostname to `swarm.cryptsync.blacklist` in server.yaml
/// and runs `vigocli config publish`. Host-level eviction across every
/// locally-enrolled user — the dispatched task targets all envoys
/// (target=*), so each envoy runs the appropriate path:
///
/// - On the blacklisted host (local hostname == --hostname): scrub
///   identity material for every locally-enrolled user. Re-enrollment
///   from a workstation is required to rejoin.
/// - On every other envoy: walk locally-enrolled users, drop the
///   blacklisted host's pubkey from each user's peer table +
///   recipient list (delegates to `apply_leave` per-user, mirroring
///   the per-user `_apply-evict` foreign path).
///
/// Idempotent: re-running on an already-blacklisted host is a no-op
/// (no identity to scrub) and re-running on an envoy that already
/// dropped the pubkey for every user is a no-op.
fn cmd_apply_blacklist(args: &[String]) -> Result<()> {
    let mut hostname: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--hostname" => {
                i += 1;
                hostname = Some(args.get(i).context("--hostname requires a value")?.clone());
                i += 1;
            }
            other => bail!("unknown flag: {}", other),
        }
    }
    let hostname = hostname.context("--hostname is required")?;

    let local_hostname = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_default()
        .trim()
        .to_string();
    let users = crate::swarm::cryptsync::enrolled_users()
        .context("enumerating locally-enrolled cryptsync users")?;

    if users.is_empty() {
        println!("(no-op) no cryptsync-enrolled users on this envoy");
        return Ok(());
    }

    let is_self = !local_hostname.is_empty() && local_hostname == hostname;
    let mut acted = 0usize;

    for user in &users {
        let home = match super::home_for_user(user) {
            Some(h) => h,
            None => continue,
        };
        if is_self {
            // Self-evict: scrub identity material for this user. Same
            // file set the per-user `_apply-evict` self-path scrubs.
            let dot = config::dot_dir(&home);
            for name in ["identity.age", "identity.salt", "recipients.txt", "config.yaml"] {
                let p = dot.join(name);
                if p.exists() {
                    if let Err(e) = std::fs::remove_file(&p) {
                        eprintln!("warning: failed to scrub {}: {}", p.display(), e);
                    }
                }
            }
            acted += 1;
        } else {
            // Foreign-evict: drop hostname's pubkey from this user's
            // peer table + regenerate recipients via apply_leave.
            // Pubkey resolved from local peer table; if the host
            // never reached this envoy's multicast for this user,
            // it's a no-op for that user (peer not in table).
            let table = PeerTable::load(user);
            let pubkey = match table.peers.get(&hostname) {
                Some(p) => p.pubkey.clone(),
                None => continue,
            };
            let leave_time = now_secs_signed();
            if let Err(e) = crate::swarm::cryptsync::apply_leave(
                user, &pubkey, &hostname, leave_time, &home,
            ) {
                eprintln!("warning: apply_leave failed for user {}: {}", user, e);
                continue;
            }
            acted += 1;
        }
    }

    if is_self {
        println!(
            "\u{2713} self-evict on {}: scrubbed cryptsync identity for {} user(s); \
             re-enroll with `vigocli swarm cryptsync enroll-fleet --user <u>` or \
             `vigo swarm cryptsync standalone enroll` (or `fleet init` if you use self-fleet) to rejoin",
            hostname, acted
        );
    } else {
        println!(
            "\u{2713} evicted {} from {} user circle(s) on this envoy",
            hostname, acted
        );
    }
    Ok(())
}

fn resolve_user_home(username: &str) -> Result<PathBuf> {
    #[cfg(unix)]
    {
        use nix::unistd::User;
        let user = User::from_name(username)
            .ok()
            .flatten()
            .with_context(|| format!("cannot resolve user {}", username))?;
        Ok(user.dir)
    }
    #[cfg(not(unix))]
    {
        bail!("cryptsync _apply-evict is Unix-only")
    }
}

// ---------- reencrypt ----------

fn cmd_reencrypt(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!("not enrolled on this envoy");
    }
    let content_dir = config::content_dir(&home);
    if !content_dir.is_dir() {
        println!("nothing to do — {} missing", content_dir.display());
        return Ok(());
    }
    if session::connect().is_err() {
        bail!(
            "session helper not running — run `vigo swarm cryptsync unlock` first; \
             reencrypt needs the identity to decrypt existing ciphertext"
        );
    }
    let recipients = config::load_recipients(&home)?;
    let username = user_for_invocation()?;

    let mut count = 0usize;
    let mut skipped = 0usize;
    for entry in fs::read_dir(&content_dir)? {
        let entry = entry?;
        let path = entry.path();
        if !path.is_file() {
            continue;
        }
        let name = match path.file_name().and_then(|n| n.to_str()) {
            Some(n) => n.to_string(),
            None => continue,
        };
        if name.starts_with('.') {
            continue;
        }
        match reencrypt_one(&path, &recipients, &username) {
            Ok(()) => count += 1,
            Err(e) => {
                eprintln!("skip {}: {}", path.display(), e);
                skipped += 1;
            }
        }
    }
    println!(
        "✓ Re-encrypted {} file(s) to {} recipient(s); skipped {}",
        count,
        recipients.len(),
        skipped
    );
    Ok(())
}

fn reencrypt_one(
    path: &Path,
    recipients: &[age::x25519::Recipient],
    username: &str,
) -> Result<()> {
    let ciphertext = fs::read(path)
        .with_context(|| format!("reading {}", path.display()))?;
    if ciphertext.is_empty() || !is_age_ciphertext(&ciphertext) {
        bail!("not age ciphertext");
    }
    let plaintext = session::request_decrypt(&ciphertext)?;

    // Write plaintext to a temp file in the same dir, then re-encrypt.
    // Use write_user_file so the ephemeral plaintext is created 0600
    // — narrows the window where another local user could read it via
    // a directory listing.
    let dir = path.parent().context("no parent")?;
    let name = path.file_name().context("no name")?.to_string_lossy().to_string();
    let plain_tmp = dir.join(format!(".{}.cryptsync.reenc.plain.tmp", name));
    let enc_tmp = dir.join(format!(".{}.cryptsync.reenc.enc.tmp", name));

    write_user_file(&plain_tmp, &plaintext, username)?;
    let result = (|| -> Result<()> {
        crypt::encrypt_file(&plain_tmp, &enc_tmp, recipients)?;
        super::finalize_user_ciphertext(&enc_tmp, path, username)?;
        Ok(())
    })();
    let _ = fs::remove_file(&plain_tmp);
    if result.is_err() {
        let _ = fs::remove_file(&enc_tmp);
    }
    result
}

fn is_age_ciphertext(bytes: &[u8]) -> bool {
    const MAGIC: &[u8] = b"age-encryption.org/v1\n";
    bytes.len() >= MAGIC.len() && &bytes[..MAGIC.len()] == MAGIC
}

/// Per-status-call breakdown of `~<user>/cryptsync/` contents.
/// Computed via a single `read_dir` scan and a one-line magic peek
/// per file — no persistent state, no quadratic work.
#[derive(Default)]
struct ContentSubcounts {
    ciphertext: usize,
    plaintext: usize,
    failed: Vec<String>,
}

impl ContentSubcounts {
    fn total(&self) -> usize {
        self.ciphertext + self.plaintext + self.failed.len()
    }
}

fn scan_content_subcounts(content: &Path) -> ContentSubcounts {
    let mut out = ContentSubcounts::default();
    let entries = match fs::read_dir(content) {
        Ok(rd) => rd,
        Err(_) => return out,
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if !path.is_file() {
            continue;
        }
        let name = match path.file_name().and_then(|n| n.to_str()) {
            Some(n) => n.to_string(),
            None => continue,
        };
        if name.starts_with('.') {
            continue;
        }
        if super::auto_encrypt::is_failed_artifact_name(&name) {
            out.failed.push(name);
            continue;
        }
        match crypt::is_age_encrypted(&path) {
            Ok(true) => out.ciphertext += 1,
            Ok(false) => out.plaintext += 1,
            Err(_) => out.plaintext += 1, // unreadable counts as not-yet-encrypted
        }
    }
    out
}

fn valid_content_name(name: &str) -> bool {
    if name.is_empty() || name.len() > 255 {
        return false;
    }
    if name.starts_with('.') {
        return false;
    }
    !name.contains('/') && !name.contains('\\') && !name.contains('\0')
}

/// Truncate-or-create write of `bytes` to `path` with mode 0600,
/// then chown to `username`. Used for sentinels (regen.pending,
/// leave.pending), the per-user config.yaml seed, and the decrypt
/// `-o` plaintext output. The chown call is idempotent — no-op when
/// the path is already user-owned (running directly as the user
/// rather than under sudo).
#[cfg(unix)]
fn write_user_file(path: &Path, bytes: &[u8], username: &str) -> Result<()> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)
        .with_context(|| format!("creating {}", path.display()))?;
    f.write_all(bytes)?;
    f.sync_all().ok();
    super::chown_to_user(path, username)
        .with_context(|| format!("chown {} to {}", path.display(), username))?;
    Ok(())
}

#[cfg(not(unix))]
fn write_user_file(path: &Path, bytes: &[u8], _username: &str) -> Result<()> {
    fs::write(path, bytes).with_context(|| format!("writing {}", path.display()))
}

// ---------- helpers ----------

/// Resolve the invoking user's username. Resolution priority is
/// chosen for robustness against env-stripping sudo configurations
/// that reset `HOME`, `USER`, and `LOGNAME` to root's values:
///
/// 1. `getuid()` if non-zero — the real uid is preserved through
///    setuid (for setuid-root installs of `vigo`) and is the most
///    authoritative signal that we're running directly for that
///    user. A non-zero ruid means `sudo` did not elevate.
/// 2. `SUDO_USER` — sudo's authoritative record of who escalated.
///    Survives `sudo --reset-environment` and `env_reset` because
///    sudo always sets it. Only consulted when `getuid()` is 0 (so
///    we don't shadow a setuid-from-user invocation).
/// 3. `USER` env var — last-resort fallback for rare setups
///    without sudo (root-direct invocation, CI containers).
///
/// Companion to `user_home_for_invocation` — both helpers MUST
/// resolve to the same user, otherwise we'd write into one user's
/// home but chown to a different user's uid.
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
    bail!("could not resolve invoking user (set $SUDO_USER or $USER, or run with non-root real uid)")
}

/// Resolve the invoking user's home directory by delegating to
/// `user_for_invocation` and then doing a passwd lookup. Avoids
/// trusting `$HOME` because sudo configs commonly strip it to
/// root's home, which would silently steer cryptsync verbs at
/// `/root/.cryptsync/` instead of the invoker's actual cryptsync state.
fn user_home_for_invocation() -> Result<PathBuf> {
    let username = user_for_invocation()?;
    super::home_for_user(&username)
        .ok_or_else(|| anyhow!("could not resolve home directory for user {}", username))
}

/// Walk up `/proc/<pid>/status::PPid` from `getppid()` past known
/// privilege-escalation wrappers (`sudo`, `env`, `su`, `doas`) and
/// our own binary, returning the first PID whose `comm` looks like
/// an actual shell or other user-controlled parent. The session
/// helper's parent-PID watchdog uses this so it survives wrapper
/// exits but still self-terminates when the user's shell goes away.
///
/// Returns `None` on Linux when the walk fails (proc unreadable,
/// hits PID 1, exceeds depth limit) — caller falls back to bare
/// `getppid()`. On macOS this always returns `None` (no `/proc`);
/// the watchdog there falls back to the immediate parent, which
/// for the typical `sudo` invocation is sudo itself, with the same
/// caveat. macOS users can rely on the idle timeout (default 60 m).
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
        let is_intermediate = intermediates.iter().any(|s| **s == comm)
            || comm == me_name;
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

fn prompt_password(confirm: bool) -> Result<String> {
    let pw = read_one_password("cryptsync password: ")?;
    if confirm {
        let pw2 = read_one_password("confirm password: ")?;
        if pw != pw2 {
            bail!("passwords do not match");
        }
    }
    Ok(pw)
}

/// Read one password line. When stdin is a TTY, use rpassword so
/// keystrokes aren't echoed. When stdin is piped (scripting,
/// automation, tests), read a plain line from stdin — no echo
/// suppression needed because the input is coming from a pipe, not a
/// terminal. Either way the password never hits argv or env.
fn read_one_password(prompt: &str) -> Result<String> {
    #[cfg(unix)]
    {
        use std::io::BufRead;
        use std::os::unix::io::AsRawFd;
        let stdin_is_tty =
            unsafe { libc::isatty(std::io::stdin().as_raw_fd()) } != 0;
        if stdin_is_tty {
            return rpassword::prompt_password(prompt).context("reading password");
        }
        let stdin = std::io::stdin();
        let mut line = String::new();
        stdin
            .lock()
            .read_line(&mut line)
            .context("reading password from stdin")?;
        Ok(line.trim_end_matches(['\r', '\n']).to_string())
    }
    #[cfg(not(unix))]
    {
        rpassword::prompt_password(prompt).context("reading password")
    }
}

/// Create-new write of `content` to `path` with mode 0600, then
/// chown to `username`. Differs from `write_user_file` in that it
/// fails if `path` already exists — used for first-time identity
/// material (identity.age, identity.salt) where overwriting would
/// be a footgun.
#[cfg(unix)]
fn write_private(path: &Path, content: &[u8], username: &str) -> Result<()> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)
        .with_context(|| format!("creating {}", path.display()))?;
    f.write_all(content)?;
    f.sync_all().ok();
    super::chown_to_user(path, username)
        .with_context(|| format!("chown {} to {}", path.display(), username))?;
    Ok(())
}

#[cfg(not(unix))]
fn write_private(path: &Path, content: &[u8], _username: &str) -> Result<()> {
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)
        .with_context(|| format!("creating {}", path.display()))?;
    f.write_all(content)?;
    f.sync_all().ok();
    Ok(())
}

/// Create `path` as a user-owned directory with the given mode if
/// it doesn't already exist. Idempotent — chowns existing dirs to
/// `username` to repair anything left over from a pre-fix run.
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
    super::chown_to_user(path, username)
        .with_context(|| format!("chown {} to {}", path.display(), username))?;
    Ok(())
}

#[cfg(not(unix))]
fn create_user_dir(path: &Path, _username: &str, _mode: u32) -> Result<()> {
    if path.is_dir() {
        return Ok(());
    }
    fs::create_dir_all(path).with_context(|| format!("creating {}", path.display()))
}

fn append_recipient(path: &Path, pubkey: &str, username: &str) -> Result<()> {
    #[cfg(unix)]
    let mut f = {
        use std::os::unix::fs::OpenOptionsExt;
        fs::OpenOptions::new()
            .create(true)
            .append(true)
            .mode(0o600)
            .open(path)
            .with_context(|| format!("opening {}", path.display()))?
    };
    #[cfg(not(unix))]
    let mut f = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
        .with_context(|| format!("opening {}", path.display()))?;
    writeln!(f, "{}", pubkey)?;
    f.sync_all().ok();
    drop(f);
    #[cfg(unix)]
    super::chown_to_user(path, username)
        .with_context(|| format!("chown {} to {}", path.display(), username))?;
    #[cfg(not(unix))]
    let _ = username;
    Ok(())
}

/// Build the self-documenting config.yaml template with the chosen
/// `wrap_mode` baked in. Written on first enrollment so the operator
/// opening ~/.cryptsync/config.yaml doesn't need to consult external
/// docs to understand what the file is for.
fn config_template(wrap_mode: config::WrapMode) -> String {
    let (mode_value, mode_blurb) = match wrap_mode {
        config::WrapMode::Standalone => (
            "standalone",
            "# wrap_mode: standalone — identity.age is wrapped under a per-envoy\n\
             # passphrase you typed at `cryptsync standalone init / enroll`. `unlock`\n\
             # prompts for it. Independent of self-fleet.",
        ),
        config::WrapMode::Fleet => (
            "fleet",
            "# wrap_mode: fleet — identity.age is wrapped under a key derived from\n\
             # this user's self-fleet identity (ADR-016). `unlock` succeeds without a\n\
             # prompt as long as the self-fleet session helper is running. Run\n\
             # `vigo swarm self-fleet unlock` first if it isn't.",
        ),
    };
    format!(
        "\
# cryptsync per-user config
#
# Lives at ~/.cryptsync/config.yaml. Read by the cryptsync daemon on
# every discovery cycle (~30s); edits take effect on the next tick
# without an agent restart. See docs/engineering/subsystems/cryptsync.md.
#
# Fields below show defaults. Uncomment + edit to override.

# Hostnames of cryptsync peers unreachable via LAN multicast (different
# subnet, NAT, VPN). The agent POSTs this envoy's announcement to each
# listed host at https://<host>:1531/swarm/cryptsync/announce in addition to
# the LAN multicast on 224.0.0.44:1533. Leave empty if every peer is on
# the same subnet as this envoy.
#
# cross_subnet_peers:
#   - envoy-london.internal
#   - envoy-tokyo.internal
cross_subnet_peers: []

# Minutes of idle time before the session helper auto-locks and scrubs
# the unwrapped identity from memory. Resets on every decrypt call.
idle_timeout_minutes: 60

# How identity.age is wrapped (set by init; do not edit by hand —
# changing this without re-enrolling will break unlock).
{mode_blurb}
wrap_mode: {mode_value}
",
        mode_blurb = mode_blurb,
        mode_value = mode_value,
    )
}

fn write_config_template(path: &Path, username: &str, wrap_mode: config::WrapMode) -> Result<()> {
    let body = config_template(wrap_mode);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(path)
            .with_context(|| format!("creating {}", path.display()))?;
        f.write_all(body.as_bytes())?;
        f.sync_all().ok();
        drop(f);
        super::chown_to_user(path, username)
            .with_context(|| format!("chown {} to {}", path.display(), username))?;
    }
    #[cfg(not(unix))]
    {
        fs::write(path, &body)
            .with_context(|| format!("writing {}", path.display()))?;
        let _ = username;
    }
    Ok(())
}
