// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! User-facing `vigo swarm lockbox <verb>` subcommand tree.
//!
//! All verbs are **user-mode** operations — they touch files under
//! the invoking user's `~/.lockbox/` and their `~/lockbox/`
//! directory. They do not require root and do not contact the
//! server or the agent daemon for Week 1 verbs.
//!
//! ```text
//! vigo swarm lockbox puddle init                   # bootstrap per-envoy age key under puddle (ADR-016, ADR-020)
//! vigo swarm lockbox status                        # show local state
//! vigo swarm lockbox unlock                        # hold key in session helper
//! vigo swarm lockbox lock                          # scrub held key
//! vigo swarm lockbox encrypt <file> [--as <name>]  # add to ~/lockbox/
//! vigo swarm lockbox decrypt <file> -o <path>      # decrypt to user path
//! vigo swarm lockbox forget <hostname>             # revoke an envoy
//! vigo swarm lockbox reencrypt                     # force re-encrypt to current recipients
//! ```
//!
//! ADR-020 retired standalone mode: lockbox requires puddle. Mode 1
//! (encryption-at-rest, no peers) is now a single-envoy puddle.

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use age::x25519::Identity;
use anyhow::{anyhow, bail, Context, Result};
use zeroize::Zeroizing;

use super::config;
use super::crypt;
use super::puddle_members;
use super::resurrects;
use super::session;
use super::state::PeerTable;
use super::tombstones;

/// Dispatch `vigo swarm lockbox <verb> [args...]`.
pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "help" | "--help" | "-h" => {
            print_usage();
            Ok(())
        }
        // Per-envoy age-key bootstrap. `puddle init` runs implicitly
        // on `puddle unlock` / `puddle join` for envoys with lockbox
        // enabled; the manual subtree is kept for recovery (ADR-020
        // retired the parallel standalone subtree).
        "puddle" => dispatch_puddle(&args[1..]),
        // Top-level verbs — all puddle-anchored post-ADR-020.
        // Recipient resolution, session helper, encryption, decryption,
        // and lifecycle ops read whatever local state exists.
        "status" => cmd_status(&args[1..]),
        "unlock" => cmd_unlock(&args[1..]),
        "lock" => cmd_lock(&args[1..]),
        "encrypt" => cmd_encrypt(&args[1..]),
        "resurrect" => cmd_resurrect(&args[1..]),
        "decrypt" => cmd_decrypt(&args[1..]),
        "delete" => cmd_delete(&args[1..]),
        "list" | "ls" => cmd_list(&args[1..]),
        "purge" => cmd_purge(&args[1..]),
        "reencrypt" => cmd_reencrypt(&args[1..]),
        // Hidden verb — invoked by the server via TaskDispatch when an
        // admin runs `vigocli lockbox evict`. Not advertised in --help.
        "_apply-evict" => cmd_apply_evict(&args[1..]),
        other => bail!(
            "unknown `vigo swarm lockbox` subcommand: {}\nrun `vigo swarm lockbox --help` for usage",
            other
        ),
    }
}

fn dispatch_puddle(args: &[String]) -> Result<()> {
    let sub = args.first().map(String::as_str).unwrap_or("");
    match sub {
        "" | "help" | "--help" | "-h" => {
            print_puddle_usage();
            Ok(())
        }
        "init" => cmd_puddle_init(&args[1..]),
        "renew-cert" => cmd_puddle_renew_cert(&args[1..]),
        other => bail!(
            "unknown `vigo swarm lockbox puddle` subcommand: {}\n\
             run `vigo swarm lockbox puddle --help` for usage",
            other
        ),
    }
}

fn print_puddle_usage() {
    eprintln!(
        "vigo swarm lockbox puddle — per-envoy age-key bootstrap (ADR-016, ADR-020)

USAGE:
    vigo swarm lockbox puddle <SUBCOMMAND>

SUBCOMMANDS:
    init          First-time setup of this user's per-envoy age key. Issues a
                  puddle-membership certificate (ADR-016) and persists it at
                  ~/.lockbox/puddle_cert.json. Requires puddle to be initialized
                  AND unlocked for this user (the cert is signed by the puddle
                  session helper). Usually auto-run by `puddle unlock` or `puddle
                  join` — running it manually is for recovery.
    renew-cert    Re-issue the puddle-membership certificate. Default lifetime 30 days;
                  certs expire and need renewal. Run from any envoy where puddle
                  is unlocked — the renewed cert ships in the next announce tick. Use
                  this after a puddle rotation, or when peers warn-log a cert
                  expiry against this envoy.

Daily verbs (unlock / encrypt / decrypt / status / leave / …) live at the top
level. See `vigo swarm lockbox --help`."
    );
}

fn print_usage() {
    eprintln!(
        "vigo swarm lockbox — per-user encrypted P2P directory

USAGE:
    vigo swarm lockbox <SUBCOMMAND>

CONTENT-DOMAIN VERBS:
    unlock                         Start session helper (puddle must be unlocked)
    lock                           Scrub held key and stop session helper
    encrypt <file> [--as <name>]   Encrypt a plaintext file into ~/lockbox/
    resurrect <file> [--as <name>] Override an active tombstone and re-encrypt in one step
    decrypt <file> -o <path>       Decrypt from ~/lockbox/ to a user-chosen path
    delete <name>                  Remove a file from ~/lockbox/; tombstone propagates to peers
    list [--long] [--tombstones]   List ciphertext files + tombstones in ~/lockbox/
    purge --yes                    Wipe ALL local lockbox state for this user (no gossip;
                                   recovery verb)
    reencrypt                      Force re-encrypt of ~/lockbox/ to current recipients
    status                         Show local lockbox state

LIFECYCLE (ADR-021, 0.40.0 — owned by puddle):
    Lifecycle verbs migrated to the puddle subtree:
      vigo swarm puddle leave        — leave the puddle (covers lockbox LEAVE +
                                       puddle scrub + gitback cleanup)
      vigocli swarm puddle evict     — admin: evict a host from a user's puddle
                                       (cross-envoy; replaces `lockbox forget`)

RECOVERY (manual, normally auto-run by `puddle unlock`):
    puddle init                     Set up per-envoy age key under puddle membership

All subcommands operate on the invoking user's home:
    ~/.lockbox/    wrapped identity, salt, recipients, config
    ~/lockbox/     ciphertext-at-rest content directory

No root required. Does not contact the Vigo server."
    );
}

// ---------- puddle init ----------
//
// Generates a per-envoy age identity wrapped under a key derived
// from the puddle session helper (ADR-016, ADR-020) and seeds
// recipients.txt. Once puddle is initialized for this user, the
// announce loop scopes recipient resolution to the puddle.
//
// Refuses if puddle has not been initialized for this user.
// Pointing the user at `vigo swarm puddle init` first is the
// correct path: lockbox puddle mode is meaningless without a puddle
// identity to scope membership to.
fn cmd_puddle_init(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if !crate::swarm::puddle::is_initialized(&home) {
        bail!(
            "puddle is not initialized for this user; lockbox requires a puddle \
             identity to scope recipient resolution and derive its wrap key (ADR-020).\n\
             Run `vigo swarm puddle init` first (single-envoy puddles are valid for \
             encryption-at-rest with no peers), then re-run this command."
        );
    }
    bootstrap_identity(&home, &username)?;
    // Auto-issue the puddle membership certificate so receivers can
    // cryptographically verify our puddle claim from the very first
    // announce. Fails noisily but doesn't undo the bootstrap — the
    // user can re-run `lockbox puddle renew-cert` separately.
    if let Err(e) = issue_puddle_cert(&home, &username) {
        eprintln!(
            "warning: lockbox puddle identity is set up but the puddle-membership cert \
             could not be issued: {:#}\n\
             Re-run `vigo swarm lockbox puddle renew-cert` once puddle is unlocked.",
            e
        );
    }
    Ok(())
}

fn cmd_puddle_renew_cert(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let username = user_for_invocation()?;
    if !crate::swarm::puddle::is_initialized(&home) {
        bail!(
            "puddle is not initialized for this user; cannot issue puddle cert.\n\
             Run `vigo swarm puddle init` first."
        );
    }
    let cs_identity = crate::swarm::lockbox::config::identity_path(&home);
    if !cs_identity.exists() {
        bail!(
            "lockbox identity is missing at {} — run `vigo swarm lockbox puddle init` first.",
            cs_identity.display()
        );
    }
    issue_puddle_cert(&home, &username)
}

/// Build, sign, and persist a fresh puddle-membership cert.
/// Reads:
///   * the local age pubkey from `~/.lockbox/recipients.txt` (first line)
///   * the local puddle pubkey from `~/.vigo-puddle/identity.pub`
/// Signs via the puddle session helper (so the user's session
/// must be unlocked when this runs). On success, writes the cert
/// to `~/.lockbox/puddle_cert.json` and chowns it to the user.
fn issue_puddle_cert(home: &Path, username: &str) -> Result<()> {
    use crate::swarm::lockbox::puddle_cert::{PuddleCert, DEFAULT_LIFETIME_SECS, SIG_LEN};

    let age_pubkey = read_local_age_pubkey(home)?;
    let puddle_pubkey = match crate::swarm::puddle::local_puddle_pubkey(home)? {
        Some(s) => s
            .strip_prefix("ed25519:")
            .map(|s| s.to_ascii_lowercase())
            .unwrap_or_else(|| s.to_ascii_lowercase()),
        None => bail!("puddle pubkey not readable; run `puddle init` first"),
    };
    let issued_at = now_secs();
    let cert = PuddleCert::issue(
        &age_pubkey,
        &puddle_pubkey,
        issued_at,
        DEFAULT_LIFETIME_SECS,
        |bytes| {
            let sig = crate::swarm::puddle::session::request_sign(bytes).map_err(|e| {
                anyhow!(
                    "puddle session helper unavailable: {}\n\
                     run `vigo swarm puddle unlock` first",
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
        let path = crate::swarm::lockbox::puddle_cert::cert_path(home);
        let _ = crate::swarm::lockbox::chown_to_user_pub(&path, username);
    }
    let _ = username; // keep parameter live on non-unix platforms
    println!(
        "✓ Issued puddle-membership cert (expires in {} days)",
        cert.days_until_expiry(issued_at)
    );
    println!(
        "  wrote {}",
        crate::swarm::lockbox::puddle_cert::cert_path(home).display()
    );
    Ok(())
}

/// Auto-refresh threshold for the puddle membership cert. If the
/// existing cert has fewer than this many days until expiry (or is
/// missing entirely), `cmd_unlock` re-issues it as a side effect of
/// the unlock. Matches the warning threshold in `cmd_status` so the
/// "expires in N day(s)" warning never fires under normal use.
const PUDDLE_CERT_REFRESH_THRESHOLD_DAYS: i64 = 7;

/// Pure predicate: true if the cert is missing OR within the refresh
/// window. Extracted so the threshold edges are unit-testable
/// independent of the puddle session helper.
fn cert_needs_refresh(
    cert: Option<&crate::swarm::lockbox::puddle_cert::PuddleCert>,
    now: i64,
) -> bool {
    match cert {
        None => true,
        Some(c) => c.days_until_expiry(now) < PUDDLE_CERT_REFRESH_THRESHOLD_DAYS,
    }
}

/// Best-effort puddle-cert refresh on `cmd_unlock`. Called only when
/// lockbox is in puddle mode and only after the puddle session
/// helper is known running (lockbox's puddle-mode unlock fails
/// earlier otherwise — see `crypt::derive_puddle_wrap_password`).
/// Surfaces failure via stderr but does not propagate; unlock has
/// already succeeded by the time we get here, and an unmintable cert
/// just means the user keeps whatever cert state they had until they
/// run `lockbox puddle renew-cert` manually.
fn refresh_puddle_cert_if_needed(home: &Path, username: &str) {
    use crate::swarm::lockbox::puddle_cert::PuddleCert;
    let now = now_secs();
    if !cert_needs_refresh(PuddleCert::load(home).as_ref(), now) {
        return;
    }
    if let Err(e) = issue_puddle_cert(home, username) {
        eprintln!(
            "warning: puddle-membership cert auto-refresh failed: {:#}",
            e
        );
        eprintln!(
            "         run `vigo swarm lockbox puddle renew-cert` to retry."
        );
    }
}

/// Pull the first non-comment line out of recipients.txt — that's
/// always this envoy's own age pubkey because `bootstrap_identity`
/// seeds it as the first entry.
fn read_local_age_pubkey(home: &Path) -> Result<String> {
    let path = crate::swarm::lockbox::config::recipients_path(home);
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

/// Generate a fresh wrapped identity, seed `recipients.txt` with the
/// freshly-minted pubkey, and create `~/lockbox/`. Per ADR-020,
/// lockbox wraps under a key derived from the puddle session
/// helper — no per-envoy passphrase prompt, no standalone branch.
/// `username` is the invoker resolved by `user_for_invocation` and
/// is used to chown every file we drop under `~/.lockbox/` and
/// `~/lockbox/` so the elevated (sudo'd) process doesn't leave
/// root-owned state in the user's home.
fn bootstrap_identity(
    home: &Path,
    username: &str,
) -> Result<()> {
    let is_init = true;
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
             To re-enroll, delete ~/.lockbox/ first (destroys decryption capability).",
            id_path.display()
        );
    }

    // Wrap-key material derived from the puddle session helper
    // (ADR-016, ADR-020): the helper signs a fixed domain-separated
    // challenge and the result is stretched through Argon2id. No
    // password prompt — the puddle session helper must be running.
    let wrap_password: Zeroizing<Vec<u8>> = crypt::derive_puddle_wrap_password().context(
        "puddle wrap-key derivation failed; ensure puddle is unlocked \
         (`vigo swarm puddle unlock`)"
    )?;

    // Generate identity + salt + wrap.
    //
    // Note: the Ed25519 signing identity for this user is generated
    // lazily by the lockbox daemon (`ensure_signing_identity`), not
    // here. The signing key authorizes DELETE envelopes and lives in
    // root-only `/var/lib/vigo/swarm/lockbox/<user>/signing.key` so
    // deletions can propagate autonomously on the daemon's schedule
    // without requiring the user to be unlocked. A compromised signing
    // key is rapidly recoverable via `vigo swarm lockbox forget` on peers;
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

    // Create ~/lockbox/ (0700, owned by the user).
    create_user_dir(&content_dir, username, 0o700)?;

    // Seed a self-documenting config.yaml if one doesn't already exist so
    // the operator opening ~/.lockbox/config.yaml can tell what it's for
    // without consulting external docs. Skip if present (don't clobber
    // operator edits) — there's no wrap_mode field to rewrite anymore.
    let cfg_path = config::config_path(home);
    if !cfg_path.exists() {
        if let Err(e) = write_config_template(&cfg_path, username) {
            tracing::warn!(
                path = %cfg_path.display(),
                error = %e,
                "lockbox: failed to seed config.yaml template (not fatal)"
            );
        }
    }

    // Remove the enroll.pending sentinel if present.
    if marker.exists() {
        if let Err(e) = fs::remove_file(&marker) {
            tracing::warn!(
                marker = %marker.display(),
                error = %e,
                "lockbox: failed to remove enroll.pending sentinel after enrollment"
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
            "lockbox: failed to drop regen.pending sentinel; recipients sync delayed until next peer change"
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
    let _ = is_init; // kept for parameter symmetry; always true post-ADR-020
    // ADR-016: each puddle member generates its own age key for
    // compartmentalization. There is no "primary" — every envoy is a
    // peer. Other puddle envoys discover this pubkey via swarm
    // announce + the puddle-membership cert.
    println!("Per-envoy lockbox identity created. Each puddle member holds its own age");
    println!("key (ADR-016: per-envoy keys preserve compartmentalization). Other envoys");
    println!("in your puddle pick up this pubkey via swarm announce + the puddle-");
    println!("membership cert — no manual enrollment.");
    println!();
    println!("To add a new envoy to the puddle:");
    println!("  1) on a current puddle envoy: `vigo swarm puddle pair`");
    println!("  2) on the new envoy:          `sudo vigo swarm puddle join --from <host>`");
    println!("  3) on the new envoy:          `vigo swarm puddle unlock` (auto-runs `lockbox puddle init`)");
    Ok(())
}

// ---------- status ----------

/// One puddle member that's expected (per `puddle_members.json`)
/// but absent from the live `recipients.txt`. Drives the per-peer
/// silent-for duration in the gap line.
pub(super) struct MissingMember {
    pub hostname: String,
    pub silent_secs: i64,
}

/// Render a silent-for duration in human terms. Values fall in
/// these bands so the gap line stays short:
///
/// - `< 120s` → seconds (`92s`)
/// - `< 1h`   → minutes (`14m`)
/// - `< 1d`   → hours (`6h`)
/// - `>= 1d`  → days (`3d`)
fn fmt_silent_for(secs: i64) -> String {
    let s = secs.max(0);
    if s < 120 {
        format!("{}s", s)
    } else if s < 3600 {
        format!("{}m", s / 60)
    } else if s < 86400 {
        format!("{}h", s / 3600)
    } else {
        format!("{}d", s / 86400)
    }
}

/// Render the `recipients:` line block for `lockbox status`.
///
/// `recipients_count` is the live recipient count from
/// `~/.lockbox/recipients.txt` — self plus peers whose last
/// announce landed in the 90s STALE_SECS window. `members_count`
/// is `Some(N)` when the user has a puddle on this envoy
/// (count of self plus every age pubkey ever observed under the
/// local puddle pubkey, sourced from `puddle_members.json`), or
/// `None` when no puddle anchor exists on this envoy (lockbox
/// hasn't been bootstrapped — no comparison possible). `missing` is the per-peer silent-for
/// detail used to render real durations rather than a bare
/// ">90s" — the table survives `prune_stale`, so the duration
/// keeps advancing even after the live peer has dropped out of
/// `peers.json`. Returns one or more lines ready to print.
///
/// Output by case:
/// - No puddle (`None`) → bare `recipients: N`. Lockbox isn't
///   bootstrapped yet (no membership concept to compare against).
/// - Puddle, all live (`recipients == members`) → bare
///   `recipients: N`. Default-healthy stays terse so operators
///   only see the expanded form when there's something to
///   notice.
/// - Puddle, gap (`recipients < members`) → multi-line "X of M
///   puddle members — 1 peer silent for 6h" (singular renders
///   the exact silent-for) or "X of M puddle members — N peers
///   silent (longest 3d)" (plural reports the worst-case
///   absence). Consequence-clarity continuation describes
///   exclusion from new encryptions and what persistence implies.
/// - Puddle, more recipients than known members
///   (`recipients > members`) → bare count + WARN log.
///   Defensive; shouldn't happen in pure puddle mode but is
///   possible in mid-mode-transition states.
fn format_recipients_line(
    recipients_count: usize,
    members_count: Option<usize>,
    missing: &[MissingMember],
) -> Vec<String> {
    match members_count {
        None => vec![format!("  recipients:       {}", recipients_count)],
        Some(m) if recipients_count == m => {
            vec![format!("  recipients:       {}", recipients_count)]
        }
        Some(m) if recipients_count > m => {
            tracing::warn!(
                recipients = recipients_count,
                members = m,
                "lockbox: recipients exceeds known puddle members; \
                 mid-mode-transition or stale state"
            );
            vec![format!("  recipients:       {}", recipients_count)]
        }
        Some(m) => {
            // recipients < members — the gap case. Pluralize
            // explicitly rather than hand-wave "(s)" suffixes.
            // Singular renders the exact silent-for; plural
            // reports the longest absence (worst-case is the
            // signal an operator wants).
            let missing_count = m - recipients_count;
            let longest = missing
                .iter()
                .map(|p| p.silent_secs)
                .max()
                .unwrap_or(super::state::STALE_SECS);
            let longest_label = fmt_silent_for(longest);
            if missing_count == 1 {
                // Single missing peer — name them so the operator
                // doesn't have to cross-reference puddle_members.json
                // by hand. Falls back to "1 peer" if the missing
                // slice doesn't carry a hostname (rare edge: count
                // mismatch from a stale read).
                let host_label = missing
                    .first()
                    .map(|p| format!("{} silent", p.hostname))
                    .unwrap_or_else(|| "1 peer silent".to_string());
                vec![
                    format!(
                        "  recipients:       {} of {} puddle members — {} for {},",
                        recipients_count, m, host_label, longest_label
                    ),
                    "                    excluded from new encryptions until it"
                        .to_string(),
                    "                    announces again. If persistent, the envoy is"
                        .to_string(),
                    "                    offline or unreachable.".to_string(),
                ]
            } else {
                vec![
                    format!(
                        "  recipients:       {} of {} puddle members — {} peers silent",
                        recipients_count, m, missing_count
                    ),
                    format!(
                        "                    (longest {}), excluded from new encryptions until",
                        longest_label
                    ),
                    "                    they announce again. If persistent, those envoys"
                        .to_string(),
                    "                    are offline or unreachable.".to_string(),
                ]
            }
        }
    }
}

fn cmd_status(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let dot = config::dot_dir(&home);
    let content = config::content_dir(&home);

    println!("lockbox status");
    println!("  home:             {}", home.display());
    println!("  dot_dir:          {}", dot.display());
    println!("  content_dir:      {}", content.display());
    println!("  enrolled:         {}", config::is_enrolled(&home));
    println!("  enroll pending:   {}", config::has_pending_enroll(&home));

    if config::is_enrolled(&home) {
        match fs::read_to_string(config::recipients_path(&home)) {
            Ok(text) => {
                // Build the live recipient set (skipping comments +
                // blanks). We need both the count for display and
                // the membership for set-difference against
                // `puddle_members.json` below.
                let recipient_set: std::collections::HashSet<String> = text
                    .lines()
                    .map(str::trim)
                    .filter(|l| !l.is_empty() && !l.starts_with('#'))
                    .map(str::to_string)
                    .collect();
                let recipients_count = recipient_set.len();

                // Compute puddle-members count + per-missing-peer
                // silent-for from `puddle_members.json` — the
                // append-only "ever-known members" table. Survives
                // `prune_stale`, so the gap line keeps rendering
                // (with a real duration, not bare ">90s") for
                // long-term-absent envoys. Self is not in the
                // table; we add 1.
                //
                // No local puddle pubkey (lockbox not bootstrapped) →
                // members_count=None, missing=empty → bare
                // `recipients: N` line.
                let (members_count, missing) =
                    if crate::swarm::puddle::is_initialized(&home) {
                        let local_pubkey =
                            crate::swarm::lockbox::local_puddle_pubkey_hex(&home);
                        if local_pubkey.is_empty() {
                            (None, Vec::new())
                        } else {
                            let username = user_for_invocation()?;
                            let table = puddle_members::PuddleMembersTable::load_user(
                                &username, &home,
                            );
                            let now = crate::swarm::lockbox::now_secs_pub();
                            let mut missing: Vec<MissingMember> = Vec::new();
                            for (age_pubkey, rec) in &table.members {
                                if !recipient_set.contains(age_pubkey) {
                                    missing.push(MissingMember {
                                        hostname: rec.hostname.clone(),
                                        silent_secs: (now - rec.last_seen).max(0),
                                    });
                                }
                            }
                            (Some(1 + table.members.len()), missing)
                        }
                    } else {
                        (None, Vec::new())
                    };

                for line in
                    format_recipients_line(recipients_count, members_count, &missing)
                {
                    println!("{}", line);
                }
            }
            Err(_) => {
                if crate::swarm::puddle::is_initialized(&home) {
                    println!(
                        "  recipients:       (recipients.txt missing) — encryption can't"
                    );
                    println!(
                        "                    proceed without a recipient set. Re-run"
                    );
                    println!(
                        "                    `vigo swarm lockbox puddle init` to repair."
                    );
                } else {
                    println!(
                        "  recipients:       (recipients.txt missing) — encryption can't"
                    );
                    println!(
                        "                    proceed without a recipient set. Re-run"
                    );
                    println!(
                        "                    `vigo swarm lockbox puddle init` to repair."
                    );
                }
            }
        }
    }

    if content.is_dir() {
        let counts = scan_content_subcounts(&content);
        println!("  files:            {}", counts.total());
        println!("    ciphertext:     {}", counts.ciphertext);
        if counts.plaintext > 0 {
            println!(
                "    plaintext:      {} — still in the clear on disk. Run",
                counts.plaintext
            );
            println!(
                "                    `vigo swarm lockbox unlock` to encrypt"
            );
            println!(
                "                    them in place and propagate to puddle members."
            );
        } else {
            println!("    plaintext:      0");
        }
        if counts.failed.is_empty() {
            println!("    failed:         0");
        } else {
            println!(
                "    failed:         {} — the watcher couldn't process these",
                counts.failed.len()
            );
            println!(
                "                    files. Inspect each path and remove or"
            );
            println!(
                "                    correct it; the watcher will retry on the"
            );
            println!(
                "                    next write or unlock."
            );
            for name in &counts.failed {
                println!("                      {}", name);
            }
        }
    } else {
        println!(
            "  files:            (content dir missing) — `~/lockbox/` has been"
        );
        println!(
            "                    deleted; nothing will sync until you recreate"
        );
        println!(
            "                    it. Run `mkdir ~/lockbox` to restore."
        );
    }

    // Tombstone count. The canonical table lives in
    // /var/lib/vigo/swarm/lockbox/<user>/tombstones.json (root 0600), but the
    // daemon mirrors every save to ~/.lockbox/tombstones.json (user
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

    // ADR-016 follow-up: puddle membership cert state. Surface this
    // when puddle is initialized for the user (otherwise lockbox
    // hasn't been bootstrapped yet on this envoy).
    if crate::swarm::puddle::is_initialized(&home) {
        match crate::swarm::lockbox::puddle_cert::PuddleCert::load(&home) {
            None => {
                println!(
                    "  puddle cert:      (not issued) — current members keep syncing"
                );
                println!(
                    "                    fine, but no new envoys can join the puddle"
                );
                println!(
                    "                    until a cert is issued. Run"
                );
                println!(
                    "                    `vigo swarm lockbox puddle renew-cert`."
                );
            }
            Some(cert) => {
                let now = now_secs();
                let days = cert.days_until_expiry(now);
                if days < 0 {
                    println!(
                        "  puddle cert:      EXPIRED {} day(s) ago — current members",
                        -days
                    );
                    println!(
                        "                    still recognize this envoy from before"
                    );
                    println!(
                        "                    expiry, but new puddle members can't"
                    );
                    println!(
                        "                    verify it and fresh recipient sets won't"
                    );
                    println!(
                        "                    include it. Run"
                    );
                    println!(
                        "                    `vigo swarm lockbox puddle renew-cert` to restore."
                    );
                } else if days < 7 {
                    println!(
                        "  puddle cert:      expires in {} day(s) — peers will stop",
                        days
                    );
                    println!(
                        "                    accepting this envoy as a new puddle"
                    );
                    println!(
                        "                    recipient when it lapses; new envoys"
                    );
                    println!(
                        "                    joining will be unable to verify it."
                    );
                    println!(
                        "                    Run"
                    );
                    println!(
                        "                    `vigo swarm lockbox puddle renew-cert` to extend."
                    );
                } else {
                    println!("  puddle cert:      valid (expires in {} day(s))", days);
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
            "not enrolled on this envoy — run `vigo swarm puddle init` first \
             (which auto-runs lockbox bootstrap), or `vigo swarm lockbox puddle init` \
             manually for recovery"
        );
    }
    let id_path = config::identity_path(&home);
    let salt_path = config::salt_path(&home);
    let blob = fs::read(&id_path)
        .with_context(|| format!("reading {}", id_path.display()))?;
    let salt = fs::read(&salt_path)
        .with_context(|| format!("reading {}", salt_path.display()))?;

    // Wrap-key sourced from the puddle session helper (ADR-016, ADR-020).
    // The helper signs a fixed domain-separated challenge; the
    // signature stretches through Argon2id into the AEAD key. No
    // password prompt — failure to source the key means puddle isn't
    // unlocked.
    let cfg = config::load_config(&home);
    let wrap_password: Zeroizing<Vec<u8>> = crypt::derive_puddle_wrap_password().context(
        "lockbox unlock requires the puddle session helper to be running; \
         run `vigo swarm puddle unlock` first",
    )?;
    let identity = crypt::unwrap_identity(&blob, wrap_password.as_slice(), &salt)?;

    // Parent shell PID — so the session helper can self-terminate
    // when the shell exits. Naive `getppid()` returns the immediate
    // parent, which is `sudo`, `env`, or another wrapper when the
    // user invokes `sudo env HOME=… vigo swarm lockbox unlock` (the only
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

    // Drain any plaintext that landed in ~/lockbox/ while locked
    // (ADR-012 unlock-sweep). Best-effort: each per-file failure is
    // surfaced inline; a partial sweep doesn't fail the unlock.
    let username = user_for_invocation()?;
    super::sweep_plaintext_on_unlock(&home, &username);

    // The puddle session helper is by construction running here (we
    // just derived the wrap-password through it). Use the open
    // signing channel to keep the puddle membership cert fresh —
    // without this, a user who only ever unlocks would slide past
    // the 30-day boundary and silently degrade to TOFU-only puddle
    // membership.
    refresh_puddle_cert_if_needed(&home, &username);
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
    let src = file.context("usage: vigo swarm lockbox encrypt <file> [--as <name>]")?;
    let src_meta = fs::metadata(&src)
        .with_context(|| format!("stat {}", src.display()))?;
    if !src_meta.is_file() {
        bail!("{} is not a regular file", src.display());
    }
    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo swarm puddle init` first \
             (which auto-runs lockbox bootstrap), or `vigo swarm lockbox puddle init` \
             manually for recovery"
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

    // Tombstone preflight — refuse before doing any encryption work
    // when the target name has an effective tombstone (tombstone
    // exists AND no later resurrect supersedes it). Without this
    // check the encrypt would silently produce ciphertext that every
    // peer rejects with HTTP 410 Gone, leaving an orphaned local
    // ciphertext and destroyed plaintext. Override path is the
    // `resurrect` verb; the error message names it explicitly.
    let username = user_for_invocation()?;
    let preflight_tombs = tombstones::TombstoneTable::load_user(&username, &home);
    let preflight_res = resurrects::ResurrectsTable::load_user(&username, &home);
    if let Some(t) =
        tombstones::effective_tombstone(&target_name, &preflight_tombs, &preflight_res)
    {
        let now = now_secs();
        let retention_remaining_secs =
            tombstones::effective_retention_secs() - (now - t.deletion_time);
        let days_remaining = (retention_remaining_secs / (24 * 60 * 60)).max(0);
        tracing::warn!(
            user = %username,
            filename = %target_name,
            deletion_time = t.deletion_time,
            days_remaining = days_remaining,
            "lockbox: encrypt refused — filename is tombstoned"
        );
        bail!(
            "filename `{name}` is tombstoned ({days}d remaining in retention).\n\
             Run `vigo swarm lockbox resurrect <file> [--as <name>]` to override and re-encrypt.",
            name = target_name,
            days = days_remaining,
        );
    }

    let content_dir = config::content_dir(&home);
    if !content_dir.is_dir() {
        bail!(
            "{} does not exist — run `vigo swarm lockbox puddle init` first",
            content_dir.display()
        );
    }
    let dst = content_dir.join(&target_name);

    // Atomic write via temp file, so a crash mid-encrypt doesn't
    // leave a half-written ciphertext in ~/lockbox/.
    let tmp = content_dir.join(format!(".{}.lockbox.enc.tmp", target_name));
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

// ---------- resurrect ----------

/// Override an active tombstone for a filename and re-encrypt the
/// file in one step. The verb exists because lockbox tombstones
/// are filename-keyed and live for `tombstone_retention_days`
/// (default 90d): without an explicit override, peers reject any
/// re-encrypt of a deleted name with HTTP 410 Gone, and the source
/// would silently produce orphaned ciphertext that never propagates.
///
/// Pipeline:
/// 1. Validate args, resolve target_name, verify enrollment, verify
///    src exists and is a regular file. (All of this BEFORE dropping
///    the sentinel — we don't want to trigger fleet fanout for a
///    resurrect that the local encrypt would then fail.)
/// 2. Load tombstones + resurrects, check `effective_tombstone`.
///    Bail if nothing to resurrect (caller should just use `encrypt`).
/// 3. Drop `~/.lockbox/resurrect.pending` with the spec.
/// 4. Poll for the daemon to remove the sentinel — up to 60s. The
///    daemon's dedicated 1s polling tick handles the load + sign +
///    fan-out + persist within ~1-2s under steady-state load.
/// 5. Verify the local resurrect record landed in the user mirror.
/// 6. Run the encrypt path identically to `cmd_encrypt`. The
///    daemon's local resurrect record means the encrypt-time
///    preflight passes; the watcher-fired PushJob lands on every
///    peer that received the resurrect (which is "every reachable
///    peer" by the time the CLI gets here).
fn cmd_resurrect(args: &[String]) -> Result<()> {
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
            "--help" | "-h" => {
                eprintln!(
                    "vigo swarm lockbox resurrect — override a tombstone and re-encrypt\n\n\
                     USAGE:\n    vigo swarm lockbox resurrect <file> [--as <name>]\n\n\
                     Lockbox tombstones live for `tombstone_retention_days` (default 90d) \n\
                     after a delete. Within that window, encrypting a file with a tombstoned\n\
                     name silently produces ciphertext that no peer will accept (HTTP 410\n\
                     Gone). This verb persists a signed resurrect record locally, gossips it\n\
                     to every live peer, and then runs the encrypt — the file lands cleanly\n\
                     across the mesh.\n\n\
                     Refuses if the target name has no active tombstone (use `encrypt` directly)."
                );
                return Ok(());
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

    let src = file.context(
        "usage: vigo swarm lockbox resurrect <file> [--as <name>]",
    )?;
    let src_meta = fs::metadata(&src)
        .with_context(|| format!("stat {}", src.display()))?;
    if !src_meta.is_file() {
        bail!("{} is not a regular file", src.display());
    }
    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo swarm puddle init` first \
             (which auto-runs lockbox bootstrap), or `vigo swarm lockbox puddle init` \
             manually for recovery"
        );
    }

    let target_name = match as_name.clone() {
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

    let username = user_for_invocation()?;

    // Refuse if there's no active effective tombstone — operator
    // would be using the wrong verb. `encrypt` covers the no-tombstone
    // case; `resurrect` is specifically for the override.
    let tombstones = tombstones::TombstoneTable::load_user(&username, &home);
    let resurrects_pre = resurrects::ResurrectsTable::load_user(&username, &home);
    if tombstones::effective_tombstone(&target_name, &tombstones, &resurrects_pre).is_none() {
        bail!(
            "no active tombstone for `{}` — use `vigo swarm lockbox encrypt` instead",
            target_name
        );
    }

    // Drop the sentinel for the daemon to pick up on its next 1s tick.
    let pending = config::resurrect_pending_path(&home);
    let spec = ResurrectPendingSpec {
        filename: target_name.clone(),
        src_path: src.to_string_lossy().to_string(),
    };
    let raw = serde_json::to_vec_pretty(&spec)
        .context("serializing resurrect.pending")?;
    if let Some(parent) = pending.parent() {
        fs::create_dir_all(parent)
            .with_context(|| format!("creating {}", parent.display()))?;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(&pending)
            .with_context(|| format!("creating {}", pending.display()))?;
        f.write_all(&raw)?;
        f.sync_all().ok();
    }
    #[cfg(not(unix))]
    {
        fs::write(&pending, &raw)
            .with_context(|| format!("writing {}", pending.display()))?;
    }

    // Poll for daemon-side processing. The dedicated 1s tick should
    // pick this up within 1-2s; budget 60s before bailing in case the
    // daemon is stopped or wedged.
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(60);
    loop {
        if !pending.exists() {
            break;
        }
        if std::time::Instant::now() > deadline {
            let _ = fs::remove_file(&pending);
            bail!(
                "daemon did not process resurrect within 60s — \
                 check `systemctl status vigo-envoy` and try again"
            );
        }
        std::thread::sleep(std::time::Duration::from_millis(250));
    }

    // Verify the daemon actually persisted the resurrect record. If
    // the sentinel disappeared but the record didn't land (daemon
    // raced a crash, malformed sentinel, etc.), bail before encrypt
    // — the encrypt would silently produce orphaned ciphertext again.
    let resurrects_post = resurrects::ResurrectsTable::load_user(&username, &home);
    if resurrects_post.active(&target_name).is_none() {
        bail!(
            "daemon cleared the sentinel but no resurrect record landed for `{}` — \
             check the journal for `lockbox: resurrect processing failed`",
            target_name
        );
    }

    // Encrypt — same path as `cmd_encrypt`. The local resurrect
    // record means the encrypt-time preflight (loaded fresh) passes;
    // the watcher-fired PushJob fans out to peers that just received
    // the resurrect over the mesh.
    let recipients = config::load_recipients(&home)?;
    let content_dir = config::content_dir(&home);
    if !content_dir.is_dir() {
        bail!(
            "{} does not exist — re-run `vigo swarm lockbox puddle init`",
            content_dir.display()
        );
    }
    let dst = content_dir.join(&target_name);
    let tmp = content_dir.join(format!(".{}.lockbox.enc.tmp", target_name));
    if let Err(e) = crypt::encrypt_file(&src, &tmp, &recipients) {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }
    if let Err(e) = super::finalize_user_ciphertext(&tmp, &dst, &username) {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }

    println!(
        "✓ Resurrected {}; encrypted to {} recipient(s); wrote {}",
        target_name,
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
    let name = name.context("usage: vigo swarm lockbox decrypt <file> -o <path>")?;
    let out = out_path.context("-o <path> is required")?;

    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo swarm puddle init` first \
             (which auto-runs lockbox bootstrap), or `vigo swarm lockbox puddle init` \
             manually for recovery"
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
            "session helper not running — run `vigo swarm lockbox unlock` first"
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
        .context("usage: vigo swarm lockbox delete <name>")?
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
            "not enrolled on this envoy — run `vigo swarm puddle init` first \
             (which auto-runs lockbox bootstrap), or `vigo swarm lockbox puddle init` \
             manually for recovery"
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
            "not enrolled on this envoy — run `vigo swarm puddle init` first \
             (which auto-runs lockbox bootstrap), or `vigo swarm lockbox puddle init` \
             manually for recovery"
        );
    }
    let content_dir = config::content_dir(&home);

    // Load tombstone table from the user-side mirror at
    // ~/.lockbox/tombstones.json (the daemon writes this on every save
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
            tombstones::effective_retention_secs() - (now - t.deletion_time);
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

// ---------- leave (internal helper for `vigo swarm puddle leave`) ----------
//
// Self-unenrollment. Three-stage handoff:
//
// 1. CLI (user context) validates state, reads own pubkey from
//    recipients.txt, writes `~/.lockbox/leave.pending` with the leave
//    parameters, exits.
// 2. Daemon reactor (running as root) polls the sentinel every
//    announce cycle. When present: loads the daemon signing key,
//    builds + fans out the signed LEAVE envelope to every live peer,
//    records the leave in `/var/lib/vigo/swarm/lockbox/<user>/leaves.json`,
//    scrubs user-side state per the sentinel's flags, removes the
//    sentinel.
// 3. Peers receiving the envelope via `POST /lockbox/leaves/<user>`
//    verify the signature, apply locally (drop pubkey + regenerate
//    recipients + drop reencrypt.pending), persist.
//
// Offline peers miss the live fan-out but naturally drop the leaving
// envoy via STALE_SECS (90s without announcement); once this envoy
// stops announcing for user X, peers prune. Admin evict covers the
// "gone host with cached ciphertext" case via `vigocli lockbox evict`.

#[derive(Debug, Default, serde::Serialize, serde::Deserialize)]
pub struct LeavePendingSpec {
    pub age_pubkey: String,
    pub hostname: String,
    pub leave_time: i64,
    pub keep_identity: bool,
    pub purge_content: bool,
}

/// Body of `~/.lockbox/resurrect.pending`. Dropped by user-context
/// `cmd_resurrect`; consumed by the daemon's
/// `process_pending_resurrects` on its 1s polling tick. The daemon
/// loads its root-owned signing key, persists a local resurrect
/// record, fans out a signed RESURRECT envelope to live peers, and
/// removes the sentinel. The CLI awaits the removal before invoking
/// the encrypt path so the file lands on peers that have already
/// received the resurrect.
///
/// `as_name` is the target filename in `~/lockbox/` — the sentinel
/// stores it explicitly (rather than re-deriving from `src_path`)
/// so the daemon's persist + fanout uses the exact same name the
/// CLI's encrypt step will. `src_path` is informational; the CLI
/// owns the encrypt.
#[derive(Debug, Default, serde::Serialize, serde::Deserialize)]
pub struct ResurrectPendingSpec {
    pub filename: String,
    pub src_path: String,
}

/// Drop a `~/.lockbox/leave.pending` sentinel for the daemon to
/// pick up on its next reactor tick. The daemon signs the LEAVE
/// envelope, fans it out to live peers, scrubs lockbox state per
/// the sentinel's flags, and removes the sentinel.
///
/// Called from `vigo swarm puddle leave` (the canonical exit verb
/// post-ADR-021). The verb itself owns operator confirmation; this
/// function is the mechanical sentinel-drop.
pub fn enqueue_leave(
    home: &Path,
    username: &str,
    keep_identity: bool,
    purge_content: bool,
) -> Result<()> {
    if !config::is_enrolled(home) {
        bail!("not enrolled on this envoy — nothing to leave");
    }

    let pubkey = read_local_pubkey(home)?;
    let hostname = read_local_hostname()?;
    let leave_time = now_secs_signed();

    let spec = LeavePendingSpec {
        age_pubkey: pubkey,
        hostname,
        leave_time,
        keep_identity,
        purge_content,
    };
    let pending_path = config::leave_pending_path(home);
    let json = serde_json::to_string_pretty(&spec).context("serializing leave spec")?;
    write_user_file(&pending_path, json.as_bytes(), username)?;
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

// ---------- purge ----------
//
// Power-user verb: wipe ALL local lockbox state for this user on
// this envoy. Distinct from `leave --purge`:
//
//   `leave --purge`  queues a signed LEAVE envelope for the daemon to
//                    fan out to peers, then drops identity + ciphertext
//                    after the daemon processes the sentinel.
//
//   `purge`          immediate filesystem wipe with no gossip. Peers
//                    age this envoy out via STALE_SECS unless the
//                    operator runs `leave` first or announces OOB.
//
// Use case: "this envoy is being decommissioned, I want every byte
// gone right now." Refuses without `--yes`. If pending gossip
// sentinels exist (`leave.pending`, `reencrypt.pending`), the
// command notes that peers will not learn of the departure unless
// the operator follows up out-of-band.
fn cmd_purge(args: &[String]) -> Result<()> {
    let mut yes = false;
    for a in args {
        match a.as_str() {
            "--yes" | "-y" => yes = true,
            "--help" | "-h" => {
                eprintln!(
                    "vigo swarm lockbox purge — wipe ALL local lockbox state for this user\n\n\
                     USAGE:\n    vigo swarm lockbox purge --yes\n\n\
                     Removes ~/.lockbox/ (wrapped identity, recipients, certs, sentinels)\n\
                     and ~/lockbox/ (ciphertext at rest). Does NOT signal peers — run\n\
                     `leave` first if you want them to learn of the departure.\n\n\
                     Refuses without --yes. The root-owned per-user mirror at\n\
                     /var/lib/vigo/swarm/lockbox/<user>/ survives a user-mode purge;\n\
                     it ages out as the daemon stops seeing this envoy."
                );
                return Ok(());
            }
            other if other.starts_with("--") => bail!("unknown flag: {}", other),
            _ => bail!("vigo swarm lockbox purge takes no positional arguments; got {}", a),
        }
    }

    let home = user_home_for_invocation()?;
    do_purge(&home, yes, &mut std::io::stdout())
}

fn do_purge<W: Write>(home: &Path, yes: bool, out: &mut W) -> Result<()> {
    let dot = config::dot_dir(home);
    let content = config::content_dir(home);

    if !dot.exists() && !content.exists() {
        writeln!(
            out,
            "nothing to purge — neither {} nor {} exists.",
            dot.display(),
            content.display()
        )?;
        return Ok(());
    }

    if !yes {
        writeln!(
            out,
            "This will WIPE all local lockbox state for this user on this envoy:"
        )?;
        if dot.exists() {
            writeln!(
                out,
                "  {}  (wrapped identity, recipients, certs, sentinels)",
                dot.display()
            )?;
        }
        if content.exists() {
            writeln!(out, "  {}  (ciphertext at rest)", content.display())?;
        }
        writeln!(out)?;
        writeln!(
            out,
            "Re-run with --yes to proceed. Peers will NOT learn of the departure unless"
        )?;
        writeln!(
            out,
            "you also run `vigo swarm lockbox leave [--yes]` first or announce OOB."
        )?;
        return Ok(());
    }

    let leave_pending = config::leave_pending_path(home).exists();
    let reencrypt_pending = config::reencrypt_pending_path(home).exists();

    let mut wiped: Vec<PathBuf> = Vec::new();
    let mut failed: Vec<(PathBuf, String)> = Vec::new();

    for target in [dot.clone(), content.clone()] {
        if !target.exists() {
            continue;
        }
        match fs::remove_dir_all(&target) {
            Ok(()) => wiped.push(target),
            Err(e) => failed.push((target, e.to_string())),
        }
    }

    for target in &wiped {
        writeln!(out, "wiped {}", target.display())?;
    }
    for (target, err) in &failed {
        writeln!(out, "failed to wipe {}: {}", target.display(), err)?;
    }

    if leave_pending || reencrypt_pending {
        writeln!(out)?;
        writeln!(
            out,
            "note: pending gossip sentinels existed at purge time — peers will not learn"
        )?;
        writeln!(
            out,
            "      of this envoy's departure unless you announced it out-of-band."
        )?;
    }

    if !failed.is_empty() {
        bail!("purge incomplete — {} target(s) failed", failed.len());
    }
    Ok(())
}

// ---------- _apply-evict (hidden, server-dispatched) ----------

/// Server-invoked hidden verb. Runs as root via TaskDispatch when an
/// admin runs `vigocli lockbox evict`. Takes `--user <u>` + `--hostname
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
    let mut all_users = false;
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
            "--all-users" => {
                all_users = true;
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
    let hostname = hostname.context("--hostname is required")?;

    // --all-users: enumerate every locally-enrolled lockbox user
    // and apply the eviction for each. Used by the 0.41.3 admin-side
    // envoy-revoke hook so the server dispatches one task per envoy
    // instead of N per-user tasks. Per-user --user form is retained
    // for the existing `vigocli swarm puddle evict --user <u>` flow.
    if all_users {
        if user.is_some() {
            bail!("--user and --all-users are mutually exclusive");
        }
        let users = super::enrolled_users()
            .context("enumerating locally-enrolled lockbox users")?;
        if users.is_empty() {
            println!("(no-op) no lockbox-enrolled users on this envoy");
            return Ok(());
        }
        let mut errs: Vec<String> = Vec::new();
        for u in &users {
            if let Err(e) = apply_evict_one(u, &hostname, pubkey.as_deref()) {
                errs.push(format!("user {u}: {e:#}"));
            }
        }
        if !errs.is_empty() {
            bail!("partial failures: {}", errs.join("; "));
        }
        return Ok(());
    }

    let user = user.context("--user or --all-users is required")?;
    apply_evict_one(&user, &hostname, pubkey.as_deref())
}

fn apply_evict_one(user: &str, hostname: &str, pubkey: Option<&str>) -> Result<()> {

    // Self-evict path: the server dispatches `_apply-evict` to *every*
    // envoy via `target: "*"`, including the evicted host itself. The
    // evicted host can't resolve the pubkey via peer-table lookup
    // (peer tables never include self) and used to early-return as a
    // no-op, leaving identity.age intact and blocking subsequent
    // re-enrollment. Detect the self case and scrub local lockbox state
    // so the operator can re-enroll the host cleanly afterward.
    let local_hostname = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_default()
        .trim()
        .to_string();
    if !local_hostname.is_empty() && local_hostname == hostname {
        let home = resolve_user_home(user)?;
        let dot = config::dot_dir(&home);
        if !config::is_enrolled(&home) {
            println!(
                "(no-op) {} is the local host but {} is not enrolled in lockbox here",
                hostname, user
            );
            return Ok(());
        }
        // Mirror the !keep_identity scrub in process_one_leave —
        // remove the identity, salt, recipients, config; preserve
        // ~/lockbox/ ciphertext (operator can `rm -rf ~/lockbox` manually
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
            "\u{2713} self-evict on {}: scrubbed lockbox identity for user {}; \
             re-enroll with `vigo swarm lockbox puddle init` to rejoin",
            hostname, user
        );
        return Ok(());
    }

    // Foreign-evict path: resolve the pubkey from the local peer
    // table (or use the caller-supplied one) and apply the LEAVE.
    let resolved_pubkey: String = match pubkey {
        Some(p) if p != "LOOKUP" && !p.is_empty() => p.to_string(),
        _ => {
            let table = PeerTable::load(user);
            match table.peers.get(hostname) {
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
    let home = resolve_user_home(user)?;
    let leave_time = now_secs_signed();
    crate::swarm::lockbox::apply_leave(user, &resolved_pubkey, hostname, leave_time, &home)
        .context("apply_leave")?;
    let shown = &resolved_pubkey[..resolved_pubkey.len().min(12)];
    println!(
        "\u{2713} evicted {} (pubkey {}...) from {}'s lockbox circle on this envoy",
        hostname, shown, user
    );
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
        bail!("lockbox _apply-evict is Unix-only")
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
            "session helper not running — run `vigo swarm lockbox unlock` first; \
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
    let plain_tmp = dir.join(format!(".{}.lockbox.reenc.plain.tmp", name));
    let enc_tmp = dir.join(format!(".{}.lockbox.reenc.enc.tmp", name));

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

/// Per-status-call breakdown of `~<user>/lockbox/` contents.
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
/// root's home, which would silently steer lockbox verbs at
/// `/root/.lockbox/` instead of the invoker's actual lockbox state.
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
/// opening ~/.lockbox/config.yaml doesn't need to consult external
/// docs to understand what the file is for.
fn config_template() -> String {
    "\
# lockbox per-user config
#
# Lives at ~/.lockbox/config.yaml. Read by the lockbox daemon on
# every discovery cycle (~30s); edits take effect on the next tick
# without an agent restart. See docs/engineering/subsystems/lockbox.md.
#
# Fields below show defaults. Uncomment + edit to override.

# Hostnames of lockbox peers unreachable via LAN multicast (different
# subnet, NAT, VPN). The agent POSTs this envoy's announcement to each
# listed host at https://<host>:1531/swarm/lockbox/announce in addition to
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
".to_string()
}

fn write_config_template(path: &Path, username: &str) -> Result<()> {
    let body = config_template();
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

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    #[test]
    fn do_purge_no_state_exits_clean() {
        let home = tempdir().unwrap();
        let mut buf: Vec<u8> = Vec::new();
        do_purge(home.path(), true, &mut buf).unwrap();
        let out = String::from_utf8(buf).unwrap();
        assert!(out.contains("nothing to purge"));
    }

    #[test]
    fn do_purge_without_yes_lists_targets_and_does_not_wipe() {
        let home = tempdir().unwrap();
        let dot = config::dot_dir(home.path());
        let content = config::content_dir(home.path());
        fs::create_dir_all(&dot).unwrap();
        fs::create_dir_all(&content).unwrap();

        let mut buf: Vec<u8> = Vec::new();
        do_purge(home.path(), false, &mut buf).unwrap();
        let out = String::from_utf8(buf).unwrap();
        assert!(out.contains("This will WIPE"));
        assert!(out.contains(&dot.display().to_string()));
        assert!(out.contains(&content.display().to_string()));
        assert!(out.contains("Re-run with --yes"));
        // Nothing wiped.
        assert!(dot.exists());
        assert!(content.exists());
    }

    #[test]
    fn do_purge_with_yes_wipes_dot_and_content() {
        let home = tempdir().unwrap();
        let dot = config::dot_dir(home.path());
        let content = config::content_dir(home.path());
        fs::create_dir_all(&dot).unwrap();
        fs::write(dot.join("identity.age"), b"wrapped").unwrap();
        fs::create_dir_all(&content).unwrap();
        fs::write(content.join("ciphertext.age"), b"x").unwrap();

        let mut buf: Vec<u8> = Vec::new();
        do_purge(home.path(), true, &mut buf).unwrap();

        assert!(!dot.exists(), "~/.lockbox/ must be wiped");
        assert!(!content.exists(), "~/lockbox/ must be wiped");
    }

    #[test]
    fn do_purge_warns_when_leave_pending_present() {
        let home = tempdir().unwrap();
        let dot = config::dot_dir(home.path());
        fs::create_dir_all(&dot).unwrap();
        fs::write(config::leave_pending_path(home.path()), b"").unwrap();

        let mut buf: Vec<u8> = Vec::new();
        do_purge(home.path(), true, &mut buf).unwrap();
        let out = String::from_utf8(buf).unwrap();
        assert!(out.contains("pending gossip sentinels"));
        assert!(out.contains("out-of-band"));
    }

    // -----------------------------------------------------------------
    // Puddle-cert auto-refresh threshold (cmd_unlock side-effect path)
    // -----------------------------------------------------------------

    fn test_cert(expires_at: i64) -> crate::swarm::lockbox::puddle_cert::PuddleCert {
        crate::swarm::lockbox::puddle_cert::PuddleCert {
            version: 1,
            envoy_age_pubkey: "age1test".into(),
            puddle_pubkey: "0".repeat(64),
            issued_at: 0,
            expires_at,
            signature: String::new(),
        }
    }

    const TEST_NOW: i64 = 1_700_000_000;
    const ONE_DAY: i64 = 86_400;

    #[test]
    fn cert_refresh_fires_when_cert_missing() {
        assert!(cert_needs_refresh(None, TEST_NOW));
    }

    #[test]
    fn cert_refresh_fires_when_expired() {
        let cert = test_cert(TEST_NOW - ONE_DAY);
        assert!(cert_needs_refresh(Some(&cert), TEST_NOW));
    }

    #[test]
    fn cert_refresh_fires_within_window() {
        // 6 days remaining: under the 7-day threshold → refresh.
        let cert = test_cert(TEST_NOW + 6 * ONE_DAY);
        assert!(cert_needs_refresh(Some(&cert), TEST_NOW));
    }

    #[test]
    fn cert_refresh_skips_at_threshold_boundary() {
        // Exactly 7 days remaining: NOT under the threshold → keep.
        let cert = test_cert(TEST_NOW + 7 * ONE_DAY);
        assert!(!cert_needs_refresh(Some(&cert), TEST_NOW));
    }

    #[test]
    fn cert_refresh_skips_when_fresh() {
        let cert = test_cert(TEST_NOW + 30 * ONE_DAY);
        assert!(!cert_needs_refresh(Some(&cert), TEST_NOW));
    }

    // -----------------------------------------------------------------
    // format_recipients_line — recipients-vs-puddle-members gap render
    // -----------------------------------------------------------------

    fn missing(host: &str, silent_secs: i64) -> MissingMember {
        MissingMember {
            hostname: host.to_string(),
            silent_secs,
        }
    }

    #[test]
    fn recipients_line_no_puddle_renders_bare_count() {
        // No puddle anchor → no comparison possible; bare line.
        let out = format_recipients_line(3, None, &[]);
        assert_eq!(out, vec!["  recipients:       3".to_string()]);
    }

    #[test]
    fn recipients_line_puddle_all_live_renders_bare_count() {
        // Default-healthy: recipients == members → keep the line
        // unchanged so operators only see the expanded form when
        // there's something to notice.
        let out = format_recipients_line(3, Some(3), &[]);
        assert_eq!(out, vec!["  recipients:       3".to_string()]);
    }

    #[test]
    fn recipients_line_puddle_single_envoy_renders_bare_one() {
        let out = format_recipients_line(1, Some(1), &[]);
        assert_eq!(out, vec!["  recipients:       1".to_string()]);
    }

    #[test]
    fn recipients_line_puddle_one_peer_silent_renders_singular_gap() {
        let m = vec![missing("annlap", 6 * 3600)];
        let out = format_recipients_line(2, Some(3), &m);
        assert_eq!(out.len(), 4);
        assert!(out[0].contains("2 of 3 puddle members"));
        // Name the missing host + render the real duration.
        assert!(out[0].contains("annlap silent for 6h"), "got: {}", out[0]);
        // Singular pronouns + verb conjugation, joined for resilience
        // against the line where each phrase lands.
        let joined = out.join("\n");
        assert!(
            joined.contains("until it"),
            "expected singular pronoun, got:\n{}",
            joined
        );
        assert!(
            joined.contains("the envoy is"),
            "expected singular envoy reference, got:\n{}",
            joined
        );
        assert!(joined.contains("offline or unreachable"));
    }

    #[test]
    fn recipients_line_puddle_multiple_peers_silent_renders_plural_gap_with_longest() {
        let m = vec![
            missing("annlap", 95),
            missing("plex", 4 * 86400),
            missing("girlslaptop", 2 * 3600),
        ];
        let out = format_recipients_line(1, Some(4), &m);
        assert_eq!(out.len(), 4);
        assert!(out[0].contains("1 of 4 puddle members"));
        assert!(out[0].contains("3 peers silent"));
        // Longest absence (4d) drives the singular duration we
        // surface; the others are absorbed into "longest …".
        assert!(out[1].contains("longest 4d"), "got: {}", out[1]);
        assert!(out[1].contains("until"));
        let joined = out.join("\n");
        assert!(
            joined.contains("they announce again"),
            "expected plural-verb continuation, got:\n{}",
            joined
        );
        assert!(
            joined.contains("those envoys"),
            "expected plural-noun closer, got:\n{}",
            joined
        );
        assert!(joined.contains("offline or unreachable"));
    }

    #[test]
    fn recipients_line_recipients_exceeds_members_renders_bare_count() {
        // Defensive edge — shouldn't happen in steady-state puddle
        // mode. Render bare count; the WARN log surfaces the
        // anomaly without scrambling the operator with a
        // negative-gap message.
        let out = format_recipients_line(5, Some(3), &[]);
        assert_eq!(out, vec!["  recipients:       5".to_string()]);
    }

    #[test]
    fn fmt_silent_for_band_boundaries() {
        assert_eq!(fmt_silent_for(0), "0s");
        assert_eq!(fmt_silent_for(95), "95s");
        assert_eq!(fmt_silent_for(119), "119s");
        assert_eq!(fmt_silent_for(120), "2m");
        assert_eq!(fmt_silent_for(3599), "59m");
        assert_eq!(fmt_silent_for(3600), "1h");
        assert_eq!(fmt_silent_for(86399), "23h");
        assert_eq!(fmt_silent_for(86400), "1d");
        assert_eq!(fmt_silent_for(7 * 86400), "7d");
        // Negative inputs (clock skew) clamp at 0.
        assert_eq!(fmt_silent_for(-5), "0s");
    }
}
