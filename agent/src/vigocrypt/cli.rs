// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! User-facing `vigo crypt <verb>` subcommand tree.
//!
//! All verbs are **user-mode** operations — they touch files under
//! the invoking user's `~/.vigocrypt/` and their `~/vigocrypt/`
//! directory. They do not require root and do not contact the
//! server or the agent daemon for Week 1 verbs.
//!
//! ```text
//! vigo crypt init                          # primary envoy, first time
//! vigo crypt enroll                        # per-envoy, one-time
//! vigo crypt status                        # show local state
//! vigo crypt unlock                        # (week 3) hold key in session helper
//! vigo crypt lock                          # (week 3) scrub held key
//! vigo crypt encrypt <file> [--as <name>]  # (week 3) add to ~/vigocrypt/
//! vigo crypt decrypt <file> -o <path>      # (week 3) decrypt to user path
//! vigo crypt forget <hostname>             # (week 3) revoke an envoy
//! vigo crypt reencrypt                     # (week 3) force re-encrypt
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

/// Dispatch `vigo crypt <verb> [args...]`.
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
        "forget" => cmd_forget(&args[1..]),
        "reencrypt" => cmd_reencrypt(&args[1..]),
        other => bail!(
            "unknown `vigo crypt` subcommand: {}\nrun `vigo crypt --help` for usage",
            other
        ),
    }
}

fn print_usage() {
    eprintln!(
        "vigo crypt — per-user encrypted P2P directory

USAGE:
    vigo crypt <SUBCOMMAND>

SUBCOMMANDS:
    init                           Primary envoy setup (first time for this user)
    enroll                         Per-envoy setup (normally triggered by enroll.pending)
    unlock                         Prompt password, start session helper
    lock                           Scrub held key and stop session helper
    encrypt <file> [--as <name>]   Encrypt a plaintext file into ~/vigocrypt/
    decrypt <file> -o <path>       Decrypt from ~/vigocrypt/ to a user-chosen path
    forget <hostname>              Revoke an envoy from the recipient set
    reencrypt                      Force re-encrypt of ~/vigocrypt/ to current recipients
    status                         Show local vigocrypt state

All subcommands operate on the invoking user's home:
    ~/.vigocrypt/    wrapped identity, salt, recipients, config
    ~/vigocrypt/     ciphertext-at-rest content directory

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
/// freshly-minted pubkey, and create `~/vigocrypt/`. Shared between
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
             To re-enroll, delete ~/.vigocrypt/ first (destroys decryption capability).",
            id_path.display()
        );
    }

    // Prompt for the vigocrypt password. `init` confirms; `enroll`
    // accepts a single entry (the user may be typing a password they
    // established on init).
    let password = prompt_password(is_init)?;
    if password.is_empty() {
        bail!("password must not be empty");
    }

    // Generate identity + salt + wrap.
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

    // Create ~/vigocrypt/ (0700).
    create_content_dir(&content_dir)?;

    // Remove the enroll.pending sentinel if present.
    if marker.exists() {
        if let Err(e) = fs::remove_file(&marker) {
            tracing::warn!(
                marker = %marker.display(),
                error = %e,
                "vigocrypt: failed to remove enroll.pending sentinel after enrollment"
            );
        }
    }

    println!("wrote {} (0600, wrapped)", id_path.display());
    println!("wrote {} (0600)", salt_path.display());
    println!("wrote {}", rec_path.display());
    println!("created {} (0700)", content_dir.display());
    println!();
    println!("public key: {}", public);
    println!();
    if is_init {
        println!("This envoy is now the primary. Add more envoys with `vigo crypt enroll`");
        println!("after provisioning them via the user module with `vigocrypt: true`.");
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

    println!("vigocrypt status");
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

    let cfg = config::load_config(&home);
    println!("  extra_peers:      {}", cfg.extra_peers.len());
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
            "not enrolled on this envoy — run `vigo crypt init` (primary) or \
             `vigo crypt enroll` (secondary) first"
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
    let src = file.context("usage: vigo crypt encrypt <file> [--as <name>]")?;
    let src_meta = fs::metadata(&src)
        .with_context(|| format!("stat {}", src.display()))?;
    if !src_meta.is_file() {
        bail!("{} is not a regular file", src.display());
    }
    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo crypt init` (primary) or \
             `vigo crypt enroll` (secondary) first"
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
            "{} does not exist — run `vigo crypt enroll` first",
            content_dir.display()
        );
    }
    let dst = content_dir.join(&target_name);

    // Atomic write via temp file, so a crash mid-encrypt doesn't
    // leave a half-written ciphertext in ~/vigocrypt/.
    let tmp = content_dir.join(format!(".{}.vigocrypt.enc.tmp", target_name));
    if let Err(e) = crypt::encrypt_file(&src, &tmp, &recipients) {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }
    fs::rename(&tmp, &dst)
        .with_context(|| format!("rename {} → {}", tmp.display(), dst.display()))?;

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
    let name = name.context("usage: vigo crypt decrypt <file> -o <path>")?;
    let out = out_path.context("-o <path> is required")?;

    let home = user_home_for_invocation()?;
    if !config::is_enrolled(&home) {
        bail!(
            "not enrolled on this envoy — run `vigo crypt init` (primary) or \
             `vigo crypt enroll` (secondary) first"
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
            "session helper not running — run `vigo crypt unlock` first"
        );
    }
    let plaintext = session::request_decrypt(&ciphertext)?;

    // Write plaintext to -o path. If the destination file exists,
    // overwrite (user asked for it). Permissions: 0600 so a
    // default-umask shell doesn't accidentally world-read plaintext.
    write_user_file(&out, &plaintext)?;
    println!("✓ Wrote {} bytes to {}", plaintext.len(), out.display());
    Ok(())
}

// ---------- forget ----------

fn cmd_forget(args: &[String]) -> Result<()> {
    let hostname = args
        .first()
        .context("usage: vigo crypt forget <hostname>")?
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
    crate::vigocrypt::regenerate_recipients_now(&username, &home)?;

    println!("✓ Forgot {}. Recipients regenerated.", hostname);
    println!("  Run `vigo crypt reencrypt` to re-encrypt existing files");
    println!("  so the revoked host cannot decrypt them from its cached ciphertext.");
    Ok(())
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
            "session helper not running — run `vigo crypt unlock` first; \
             reencrypt needs the identity to decrypt existing ciphertext"
        );
    }
    let recipients = config::load_recipients(&home)?;

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
        match reencrypt_one(&path, &recipients) {
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

fn reencrypt_one(path: &Path, recipients: &[age::x25519::Recipient]) -> Result<()> {
    let ciphertext = fs::read(path)
        .with_context(|| format!("reading {}", path.display()))?;
    if ciphertext.is_empty() || !is_age_ciphertext(&ciphertext) {
        bail!("not age ciphertext");
    }
    let plaintext = session::request_decrypt(&ciphertext)?;

    // Write plaintext to a temp file in the same dir, then re-encrypt.
    let dir = path.parent().context("no parent")?;
    let name = path.file_name().context("no name")?.to_string_lossy().to_string();
    let plain_tmp = dir.join(format!(".{}.vigocrypt.reenc.plain.tmp", name));
    let enc_tmp = dir.join(format!(".{}.vigocrypt.reenc.enc.tmp", name));

    fs::write(&plain_tmp, &plaintext)
        .with_context(|| format!("writing {}", plain_tmp.display()))?;
    let result = (|| -> Result<()> {
        crypt::encrypt_file(&plain_tmp, &enc_tmp, recipients)?;
        fs::rename(&enc_tmp, path)
            .with_context(|| format!("rename {} → {}", enc_tmp.display(), path.display()))?;
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
    let pw = read_one_password("vigocrypt password: ")?;
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
