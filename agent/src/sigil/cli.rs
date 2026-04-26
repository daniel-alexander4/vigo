// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! User-facing `vigo sigil <verb>` subcommand tree.
//!
//! All verbs are **user-mode** operations — they touch files under
//! the invoking user's `~/.sigil/` and their `~/sigil/`
//! directory. They do not require root and do not contact the
//! server or the agent daemon for Week 1 verbs.
//!
//! ```text
//! vigo sigil init                          # primary envoy, first time
//! vigo sigil enroll                        # per-envoy, one-time
//! vigo sigil status                        # show local state
//! vigo sigil unlock                        # (week 3) hold key in session helper
//! vigo sigil lock                          # (week 3) scrub held key
//! vigo sigil encrypt <file> [--as <name>]  # (week 3) add to ~/sigil/
//! vigo sigil decrypt <file> -o <path>      # (week 3) decrypt to user path
//! vigo sigil forget <hostname>             # (week 3) revoke an envoy
//! vigo sigil reencrypt                     # (week 3) force re-encrypt
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

use super::config;
use super::crypt;
use super::session;
use super::state::PeerTable;
use super::tombstones;

/// Dispatch `vigo sigil <verb> [args...]`.
pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "help" | "--help" | "-h" => {
            print_usage();
            Ok(())
        }
        "init" => cmd_init(&args[1..]),
        "enroll" => cmd_enroll(&args[1..]),
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
        // admin runs `vigocli sigil evict`. Not advertised in --help.
        "_apply-evict" => cmd_apply_evict(&args[1..]),
        other => bail!(
            "unknown `vigo sigil` subcommand: {}\nrun `vigo sigil --help` for usage",
            other
        ),
    }
}

fn print_usage() {
    eprintln!(
        "vigo sigil — per-user encrypted P2P directory

USAGE:
    vigo sigil <SUBCOMMAND>

SUBCOMMANDS:
    init                           Primary envoy setup (first time for this user)
    enroll                         Per-envoy setup (normally triggered by enroll.pending)
    unlock                         Prompt password, start session helper
    lock                           Scrub held key and stop session helper
    encrypt <file> [--as <name>]   Encrypt a plaintext file into ~/sigil/
    decrypt <file> -o <path>       Decrypt from ~/sigil/ to a user-chosen path
    delete <name>                  Remove a file from ~/sigil/; tombstone propagates to peers
    list [--long] [--tombstones]   List ciphertext files + tombstones in ~/sigil/
    forget <hostname>              Revoke an envoy from the recipient set (local only)
    leave [--purge] [--keep-identity] [--yes]  Unenroll this user from sigil on this envoy
    reencrypt                      Force re-encrypt of ~/sigil/ to current recipients
    status                         Show local sigil state

All subcommands operate on the invoking user's home:
    ~/.sigil/    wrapped identity, salt, recipients, config
    ~/sigil/     ciphertext-at-rest content directory

No root required. Does not contact the Vigo server."
    );
}

// ---------- init ----------

fn cmd_init(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    bootstrap_identity(&home, /* is_init */ true)
}

// ---------- enroll ----------

fn cmd_enroll(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    bootstrap_identity(&home, /* is_init */ false)
}

/// Generate a fresh wrapped identity, seed `recipients.txt` with the
/// freshly-minted pubkey, and create `~/sigil/`. Shared between
/// `init` and `enroll` — the mechanics are identical, only the
/// operator-facing wording differs.
fn bootstrap_identity(home: &Path, is_init: bool) -> Result<()> {
    let dot = config::dot_dir(home);
    fs::create_dir_all(&dot)
        .with_context(|| format!("creating {}", dot.display()))?;

    let id_path = config::identity_path(home);
    let salt_path = config::salt_path(home);
    let rec_path = config::recipients_path(home);
    let content_dir = config::content_dir(home);
    let marker = config::enroll_pending_path(home);

    if id_path.exists() {
        bail!(
            "{} already exists — this envoy is already enrolled.\n\
             To re-enroll, delete ~/.sigil/ first (destroys decryption capability).",
            id_path.display()
        );
    }

    // Prompt for the sigil password. `init` confirms; `enroll`
    // accepts a single entry (the user may be typing a password they
    // established on init).
    let password = prompt_password(is_init)?;
    if password.is_empty() {
        bail!("password must not be empty");
    }

    // Generate identity + salt + wrap.
    //
    // Note: the Ed25519 signing identity for this user is generated
    // lazily by the sigil daemon (`ensure_signing_identity`), not
    // here. The signing key authorizes DELETE envelopes and lives in
    // root-only `/var/lib/vigo/sigil/<user>/signing.key` so
    // deletions can propagate autonomously on the daemon's schedule
    // without requiring the user to be unlocked. A compromised signing
    // key is rapidly recoverable via `vigo sigil forget` on peers;
    // a compromised encryption key is not, which is why only the
    // encryption identity is password-wrapped.
    let identity = Identity::generate();
    let public = identity.to_public();
    let salt = crypt::fresh_salt();
    let blob = crypt::wrap_identity(&identity, password.as_bytes(), &salt)
        .context("wrapping identity")?;

    // Write wrapped identity + salt (0600).
    write_private(&id_path, &blob)?;
    write_private(&salt_path, &salt)?;

    // Seed recipients.txt with this envoy's pubkey.
    append_recipient(&rec_path, &public.to_string())?;

    // Create ~/sigil/ (0700).
    create_content_dir(&content_dir)?;

    // Seed a self-documenting config.yaml if one doesn't already exist so
    // the operator opening ~/.sigil/config.yaml can tell what it's for
    // without consulting external docs. Skip if present (don't clobber
    // operator edits).
    let cfg_path = config::config_path(home);
    if !cfg_path.exists() {
        if let Err(e) = write_config_template(&cfg_path) {
            tracing::warn!(
                path = %cfg_path.display(),
                error = %e,
                "sigil: failed to seed config.yaml template (not fatal)"
            );
        }
    }

    // Remove the enroll.pending sentinel if present.
    if marker.exists() {
        if let Err(e) = fs::remove_file(&marker) {
            tracing::warn!(
                marker = %marker.display(),
                error = %e,
                "sigil: failed to remove enroll.pending sentinel after enrollment"
            );
        }
    }

    // Drop regen.pending so the daemon reactor folds existing live
    // peers into the freshly-seeded recipients.txt on its next tick.
    // Without this, recipients.txt would stay at "self only" until
    // a peer rotates its pubkey or ages out — heartbeat announces
    // from already-known peers don't trigger a regen.
    let regen_marker = config::regen_pending_path(home);
    if let Err(e) = fs::OpenOptions::new()
        .create(true)
        .write(true)
        .truncate(true)
        .open(&regen_marker)
    {
        tracing::warn!(
            path = %regen_marker.display(),
            error = %e,
            "sigil: failed to drop regen.pending sentinel; recipients sync delayed until next peer change"
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
        println!("This envoy is now the primary. Add more envoys with `vigo sigil enroll`");
        println!("after provisioning them via the user's usercrate with `sigil: true`.");
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

    println!("sigil status");
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
        let file_count = fs::read_dir(&content)
            .map(|rd| {
                rd.filter_map(|e| e.ok())
                    .filter(|e| e.path().is_file())
                    .filter(|e| {
                        e.file_name()
                            .to_str()
                            .map(|n| !n.starts_with('.'))
                            .unwrap_or(false)
                    })
                    .count()
            })
            .unwrap_or(0);
        println!("  files:            {}", file_count);
    } else {
        println!("  files:            (content dir missing)");
    }

    // Tombstone count. The canonical table lives in
    // /var/lib/vigo/sigil/<user>/tombstones.json (root 0600), but the
    // daemon mirrors every save to ~/.sigil/tombstones.json (user
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
            "not enrolled on this envoy — run `vigo sigil init` (primary) or \
             `vigo sigil enroll` (secondary) first"
        );
    }
    let id_path = config::identity_path(&home);
    let salt_path = config::salt_path(&home);
    let blob = fs::read(&id_path)
        .with_context(|| format!("reading {}", id_path.display()))?;
    let salt = fs::read(&salt_path)
        .with_context(|| format!("reading {}", salt_path.display()))?;

    let password = prompt_password(false)?;
    if password.is_empty() {
        bail!("password must not be empty");
    }
    let identity = crypt::unwrap_identity(&blob, password.as_bytes(), &salt)?;

    // Parent shell PID — so the session helper can self-terminate
    // when the shell exits. We are ourselves a child of the shell,
    // so getppid() = shell's PID.
    let parent_pid = unsafe { libc::getppid() } as u32;
    let cfg = config::load_config(&home);
    session::spawn_helper(&identity, parent_pid, cfg.idle_timeout_minutes)?;

    println!("✓ Unlocked. Session helper running until logout, lock, or {} min idle.",
        cfg.idle_timeout_minutes);
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
    let src = file.context("usage: vigo sigil encrypt <file> [--as <name>]")?;
    let src_meta = fs::metadata(&src)
        .with_context(|| format!("stat {}", src.display()))?;
    if !src_meta.is_file() {
        bail!("{} is not a regular file", src.display());
    }
    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo sigil init` (primary) or \
             `vigo sigil enroll` (secondary) first"
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
            "{} does not exist — run `vigo sigil enroll` first",
            content_dir.display()
        );
    }
    let dst = content_dir.join(&target_name);
    let username = user_for_invocation()?;

    // Atomic write via temp file, so a crash mid-encrypt doesn't
    // leave a half-written ciphertext in ~/sigil/.
    let tmp = content_dir.join(format!(".{}.sigil.enc.tmp", target_name));
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
    let name = name.context("usage: vigo sigil decrypt <file> -o <path>")?;
    let out = out_path.context("-o <path> is required")?;

    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo sigil init` (primary) or \
             `vigo sigil enroll` (secondary) first"
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
            "session helper not running — run `vigo sigil unlock` first"
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
    write_user_file(&out, &plaintext)?;
    #[cfg(unix)]
    {
        let username = user_for_invocation()?;
        if let Err(e) = super::chown_to_user(&out, &username) {
            // Non-fatal — the file is at least 0600 and contains the
            // user's own decrypted content. They can chown manually
            // if needed.
            tracing::warn!(
                path = %out.display(),
                user = %username,
                error = %e,
                "sigil decrypt: chown to invoking user failed (file is 0600 root-owned)"
            );
        }
    }
    println!("✓ Wrote {} bytes to {}", plaintext.len(), out.display());
    Ok(())
}

// ---------- delete ----------

fn cmd_delete(args: &[String]) -> Result<()> {
    let name = args
        .first()
        .context("usage: vigo sigil delete <name>")?
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
            "not enrolled on this envoy — run `vigo sigil init` (primary) or \
             `vigo sigil enroll` (secondary) first"
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
            "not enrolled on this envoy — run `vigo sigil init` (primary) or \
             `vigo sigil enroll` (secondary) first"
        );
    }
    let content_dir = config::content_dir(&home);

    // Load tombstone table from the user-side mirror at
    // ~/.sigil/tombstones.json (the daemon writes this on every save
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
        .context("usage: vigo sigil forget <hostname>")?
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
    crate::sigil::regenerate_recipients_now(&username, &home)?;

    println!("✓ Forgot {}. Recipients regenerated.", hostname);
    println!("  Run `vigo sigil reencrypt` to re-encrypt existing files");
    println!("  so the revoked host cannot decrypt them from its cached ciphertext.");
    Ok(())
}

// ---------- leave ----------
//
// Self-unenrollment. Three-stage handoff:
//
// 1. CLI (user context) validates state, reads own pubkey from
//    recipients.txt, writes `~/.sigil/leave.pending` with the leave
//    parameters, exits.
// 2. Daemon reactor (running as root) polls the sentinel every
//    announce cycle. When present: loads the daemon signing key,
//    builds + fans out the signed LEAVE envelope to every live peer,
//    records the leave in `/var/lib/vigo/sigil/<user>/leaves.json`,
//    scrubs user-side state per the sentinel's flags, removes the
//    sentinel.
// 3. Peers receiving the envelope via `POST /sigil/leaves/<user>`
//    verify the signature, apply locally (drop pubkey + regenerate
//    recipients + drop reencrypt.pending), persist.
//
// Offline peers miss the live fan-out but naturally drop the leaving
// envoy via STALE_SECS (90s without announcement); once this envoy
// stops announcing for user X, peers prune. Admin evict covers the
// "gone host with cached ciphertext" case via `vigocli sigil evict`.

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
                    "vigo sigil leave — unenroll this user's sigil on this envoy\n\n\
                     USAGE:\n    vigo sigil leave [--purge] [--keep-identity] [--yes]\n\n\
                     FLAGS:\n    \
                     --purge          also delete ~/sigil/ ciphertext (default: keep, orphaned)\n    \
                     --keep-identity  keep ~/.sigil/ so a re-enroll can reuse the wrapped\n                     \
                     identity. Default is to scrub.\n    \
                     --yes            skip the confirmation prompt"
                );
                return Ok(());
            }
            other if other.starts_with("--") => bail!("unknown flag: {}", other),
            _ => bail!("vigo sigil leave takes no positional arguments; got {}", a),
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

    println!("This will unenroll this envoy from your sigil circle.");
    println!("  pubkey:   {}", pubkey);
    println!("  hostname: {}", hostname);
    if purge_content {
        println!("  ~/sigil/ ciphertext will be DELETED (--purge).");
    } else {
        println!("  ~/sigil/ ciphertext will be kept (orphaned — you can't decrypt here).");
    }
    if keep_identity {
        println!("  ~/.sigil/ wrapped identity will be KEPT (--keep-identity).");
    } else {
        println!("  ~/.sigil/ wrapped identity will be scrubbed.");
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
    fs::write(&pending_path, json)
        .with_context(|| format!("writing {}", pending_path.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(&pending_path, fs::Permissions::from_mode(0o600));
    }

    println!();
    println!("\u{2713} leave queued at {}", pending_path.display());
    println!("  the agent will sign + propagate the LEAVE within one announce cycle (~30 s).");
    println!("  Run `vigo sigil status` afterwards to confirm the sentinel cleared.");
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
/// admin runs `vigocli sigil evict`. Takes `--user <u>` + `--hostname
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
    // re-enrollment. Detect the self case and scrub local sigil state
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
                "(no-op) {} is the local host but {} is not enrolled in sigil here",
                hostname, user
            );
            return Ok(());
        }
        // Mirror the !keep_identity scrub in process_one_leave —
        // remove the identity, salt, recipients, config; preserve
        // ~/sigil/ ciphertext (operator can `rm -rf ~/sigil` manually
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
            "\u{2713} self-evict on {}: scrubbed sigil identity for user {}; \
             re-enroll with `vigo sigil enroll` to rejoin",
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
    crate::sigil::apply_leave(&user, &pubkey, &hostname, leave_time, &home)
        .context("apply_leave")?;
    let shown = &pubkey[..pubkey.len().min(12)];
    println!(
        "\u{2713} evicted {} (pubkey {}...) from {}'s sigil circle on this envoy",
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
        bail!("sigil _apply-evict is Unix-only")
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
            "session helper not running — run `vigo sigil unlock` first; \
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
    let plain_tmp = dir.join(format!(".{}.sigil.reenc.plain.tmp", name));
    let enc_tmp = dir.join(format!(".{}.sigil.reenc.enc.tmp", name));

    write_user_file(&plain_tmp, &plaintext)?;
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

fn valid_content_name(name: &str) -> bool {
    if name.is_empty() || name.len() > 255 {
        return false;
    }
    if name.starts_with('.') {
        return false;
    }
    !name.contains('/') && !name.contains('\\') && !name.contains('\0')
}

#[cfg(unix)]
fn write_user_file(path: &Path, bytes: &[u8]) -> Result<()> {
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
    Ok(())
}

#[cfg(not(unix))]
fn write_user_file(path: &Path, bytes: &[u8]) -> Result<()> {
    fs::write(path, bytes).with_context(|| format!("writing {}", path.display()))
}

// ---------- helpers ----------

/// Resolve the invoking user's username — `SUDO_USER` (when running
/// under sudo, the typical case for `vigo sigil` since the agent
/// binary lives in `/usr/local/sbin`), falling back to a `getuid()`
/// passwd lookup. Companion to `user_home_for_invocation` — used so
/// chown-to-user calls during ciphertext finalization point at the
/// real invoker rather than the elevated process's effective uid.
fn user_for_invocation() -> Result<String> {
    if let Ok(s) = std::env::var("SUDO_USER") {
        if !s.is_empty() && s != "root" {
            return Ok(s);
        }
    }
    #[cfg(unix)]
    {
        let uid = unsafe { libc::getuid() };
        if let Ok(passwd) = fs::read_to_string("/etc/passwd") {
            for line in passwd.lines() {
                let fields: Vec<&str> = line.split(':').collect();
                if fields.len() >= 7 {
                    if let Ok(u) = fields[2].parse::<u32>() {
                        if u == uid {
                            return Ok(fields[0].to_string());
                        }
                    }
                }
            }
        }
    }
    bail!("could not resolve invoking user (set $SUDO_USER)")
}

/// Resolve the invoking user's home directory.
fn user_home_for_invocation() -> Result<PathBuf> {
    if let Some(h) = std::env::var_os("HOME").map(PathBuf::from) {
        if !h.as_os_str().is_empty() {
            return Ok(h);
        }
    }
    #[cfg(unix)]
    {
        let uid = unsafe { libc::getuid() };
        if let Ok(passwd) = fs::read_to_string("/etc/passwd") {
            for line in passwd.lines() {
                let fields: Vec<&str> = line.split(':').collect();
                if fields.len() >= 7 {
                    if let Ok(u) = fields[2].parse::<u32>() {
                        if u == uid {
                            return Ok(PathBuf::from(fields[5]));
                        }
                    }
                }
            }
        }
    }
    Err(anyhow!("could not resolve home directory (set $HOME)"))
}

fn prompt_password(confirm: bool) -> Result<String> {
    let pw = read_one_password("sigil password: ")?;
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

#[cfg(unix)]
fn write_private(path: &Path, content: &[u8]) -> Result<()> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)
        .with_context(|| format!("creating {}", path.display()))?;
    f.write_all(content)?;
    f.sync_all().ok();
    Ok(())
}

#[cfg(not(unix))]
fn write_private(path: &Path, content: &[u8]) -> Result<()> {
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)
        .with_context(|| format!("creating {}", path.display()))?;
    f.write_all(content)?;
    f.sync_all().ok();
    Ok(())
}

#[cfg(unix)]
fn create_content_dir(path: &Path) -> Result<()> {
    use std::os::unix::fs::DirBuilderExt;
    if path.is_dir() {
        return Ok(());
    }
    std::fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(path)
        .with_context(|| format!("creating {}", path.display()))
}

#[cfg(not(unix))]
fn create_content_dir(path: &Path) -> Result<()> {
    if path.is_dir() {
        return Ok(());
    }
    fs::create_dir_all(path).with_context(|| format!("creating {}", path.display()))
}

fn append_recipient(path: &Path, pubkey: &str) -> Result<()> {
    let mut f = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
        .with_context(|| format!("opening {}", path.display()))?;
    writeln!(f, "{}", pubkey)?;
    f.sync_all().ok();
    Ok(())
}

/// Self-documenting config.yaml template. Written on first enrollment so
/// the operator opening ~/.sigil/config.yaml doesn't need to consult
/// external docs to understand what the file is for.
const CONFIG_TEMPLATE: &str = "\
# sigil per-user config
#
# Lives at ~/.sigil/config.yaml. Read by the sigil daemon on
# every discovery cycle (~30s); edits take effect on the next tick
# without an agent restart. See docs/engineering/subsystems/sigil.md.
#
# Fields below show defaults. Uncomment + edit to override.

# Hostnames of sigil peers unreachable via LAN multicast (different
# subnet, NAT, VPN). The agent POSTs this envoy's announcement to each
# listed host at https://<host>:1531/sigil/announce in addition to
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
";

fn write_config_template(path: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(path)
            .with_context(|| format!("creating {}", path.display()))?;
        f.write_all(CONFIG_TEMPLATE.as_bytes())?;
        f.sync_all().ok();
    }
    #[cfg(not(unix))]
    {
        fs::write(path, CONFIG_TEMPLATE)
            .with_context(|| format!("writing {}", path.display()))?;
    }
    Ok(())
}
