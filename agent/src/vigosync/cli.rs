// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! User-facing `vigo vigosync <verb>` subcommand tree.
//!
//! All verbs are **user-mode** operations — they touch files under
//! the invoking user's `~/.vigosync/` and their `~/vigosync/`
//! directory. They do not require root and do not contact the
//! server or the agent daemon.
//!
//! ```text
//! vigo vigosync init                        # generate keypair + recipients
//! vigo vigosync decrypt <file> [-o <dest>]  # decrypt one file
//! vigo vigosync recipients list             # list recipients
//! vigo vigosync recipients add <pubkey>     # add a recipient
//! vigo vigosync recipients remove <pubkey>  # remove a recipient
//! vigo vigosync reencrypt                   # re-encrypt all files after recipient change
//! ```
//!
//! The `init` verb refuses to overwrite an existing `identity.txt`;
//! recovery after loss of the identity file is by design a manual
//! step (restore from backup or generate fresh keys + `reencrypt`).

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use age::secrecy::ExposeSecret;
use age::x25519::Identity;
use anyhow::{anyhow, bail, Context, Result};

use super::config;
use super::crypt;

/// Dispatch `vigo vigosync <verb> [args...]`.
pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "help" | "--help" | "-h" => {
            print_usage();
            Ok(())
        }
        "init" => cmd_init(&args[1..]),
        "decrypt" => cmd_decrypt(&args[1..]),
        "recipients" => dispatch_recipients(&args[1..]),
        "reencrypt" => cmd_reencrypt(&args[1..]),
        other => bail!(
            "unknown `vigo vigosync` subcommand: {}\nrun `vigo vigosync --help` for usage",
            other
        ),
    }
}

fn print_usage() {
    eprintln!(
        "vigo vigosync — manage encrypted vigosync directories

USAGE:
    vigo vigosync <SUBCOMMAND>

SUBCOMMANDS:
    init                           Generate age keypair + recipients file
    decrypt <file> [-o <dest>]     Decrypt a file (stdout if no -o)
    recipients list                List configured recipients
    recipients add <pubkey>        Add a recipient (age1...)
    recipients remove <pubkey>     Remove a recipient
    reencrypt                      Re-encrypt every file with current recipients

All subcommands operate on the invoking user's home: ~/.vigosync/ for
keys + recipients, ~/vigosync/ for files. No root required."
    );
}

// ---------- init ----------

fn cmd_init(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let dot = config::dot_dir(&home);
    fs::create_dir_all(&dot)
        .with_context(|| format!("creating {}", dot.display()))?;

    let id_path = config::identity_path(&home);
    let rec_path = config::recipients_path(&home);

    if id_path.exists() {
        bail!(
            "{} already exists — refusing to overwrite. Move or delete it first.",
            id_path.display()
        );
    }

    let identity = Identity::generate();
    let public = identity.to_public();

    // Write identity.txt with 0600 perms.
    write_private(&id_path, identity.to_string().expose_secret())?;

    // Seed recipients.txt with the freshly generated pubkey (append if
    // the file already exists — e.g. an operator pre-staged a backup
    // pubkey from another machine).
    append_recipient(&rec_path, &public.to_string())?;

    println!("wrote {} (0600)", id_path.display());
    println!("wrote {}", rec_path.display());
    println!();
    println!("public key: {}", public);
    println!();
    println!("Keep identity.txt private — anyone with this file can decrypt your");
    println!("vigosync content. To add another device that should also be able to");
    println!("decrypt, run `vigo vigosync init` on that device and append its public");
    println!("key to recipients.txt on every device (or use `vigo vigosync recipients");
    println!("add <pubkey>` + `vigo vigosync reencrypt` to re-encrypt existing files).");
    Ok(())
}

// ---------- decrypt ----------

fn cmd_decrypt(args: &[String]) -> Result<()> {
    let mut file: Option<PathBuf> = None;
    let mut out: Option<PathBuf> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "-o" | "--output" => {
                i += 1;
                out = Some(PathBuf::from(
                    args.get(i).context("-o requires a path")?,
                ));
            }
            s if s.starts_with('-') => bail!("unknown flag: {}", s),
            s => {
                if file.is_some() {
                    bail!("only one file accepted");
                }
                file = Some(PathBuf::from(s));
            }
        }
        i += 1;
    }
    let file = file.context("usage: vigo vigosync decrypt <file> [-o <dest>]")?;
    let home = user_home_for_invocation()?;
    let identity = config::load_identity(&home)?;

    match out {
        Some(dest) => {
            let mut f = fs::File::create(&dest)
                .with_context(|| format!("creating {}", dest.display()))?;
            crypt::decrypt_to_writer(&file, &identity, &mut f)?;
            f.sync_all().ok();
            eprintln!("decrypted {} → {}", file.display(), dest.display());
        }
        None => {
            let mut stdout = std::io::stdout().lock();
            crypt::decrypt_to_writer(&file, &identity, &mut stdout)?;
            stdout.flush().ok();
        }
    }
    Ok(())
}

// ---------- recipients ----------

fn dispatch_recipients(args: &[String]) -> Result<()> {
    let sub = args.first().map(String::as_str).unwrap_or("");
    match sub {
        "" | "help" | "--help" | "-h" => {
            eprintln!("usage: vigo vigosync recipients {{list|add <pubkey>|remove <pubkey>}}");
            Ok(())
        }
        "list" => recipients_list(),
        "add" => recipients_add(&args[1..]),
        "remove" | "rm" => recipients_remove(&args[1..]),
        other => bail!("unknown recipients verb: {}", other),
    }
}

fn recipients_list() -> Result<()> {
    let home = user_home_for_invocation()?;
    let path = config::recipients_path(&home);
    if !path.exists() {
        eprintln!("no recipients yet — run `vigo vigosync init` first");
        return Ok(());
    }
    let text = fs::read_to_string(&path)?;
    for line in text.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') {
            continue;
        }
        println!("{}", trimmed);
    }
    Ok(())
}

fn recipients_add(args: &[String]) -> Result<()> {
    let pubkey = args
        .first()
        .context("usage: vigo vigosync recipients add <age1...>")?;
    // Validate — reject malformed pubkeys early.
    let _: age::x25519::Recipient = pubkey
        .parse()
        .map_err(|e: &str| anyhow!("not a valid age recipient: {}", e))?;

    let home = user_home_for_invocation()?;
    let dot = config::dot_dir(&home);
    fs::create_dir_all(&dot)?;
    let path = config::recipients_path(&home);

    // Already present? No-op.
    if let Ok(text) = fs::read_to_string(&path) {
        if text.lines().any(|l| l.trim() == pubkey) {
            eprintln!("{} already present in {}", pubkey, path.display());
            return Ok(());
        }
    }
    append_recipient(&path, pubkey)?;
    eprintln!("added {} to {}", pubkey, path.display());
    eprintln!("run `vigo vigosync reencrypt` to re-encrypt existing files with the new recipient set");
    Ok(())
}

fn recipients_remove(args: &[String]) -> Result<()> {
    let pubkey = args
        .first()
        .context("usage: vigo vigosync recipients remove <age1...>")?;
    let home = user_home_for_invocation()?;
    let path = config::recipients_path(&home);
    if !path.exists() {
        bail!("{} does not exist", path.display());
    }
    let text = fs::read_to_string(&path)?;
    let mut found = false;
    let mut out = String::new();
    for line in text.lines() {
        if line.trim() == pubkey {
            found = true;
            continue;
        }
        out.push_str(line);
        out.push('\n');
    }
    if !found {
        bail!("{} is not in {}", pubkey, path.display());
    }
    fs::write(&path, out).with_context(|| format!("writing {}", path.display()))?;
    eprintln!("removed {} from {}", pubkey, path.display());
    eprintln!("run `vigo vigosync reencrypt` so receivers holding only the old key lose access");
    Ok(())
}

// ---------- reencrypt ----------

fn cmd_reencrypt(_args: &[String]) -> Result<()> {
    let home = user_home_for_invocation()?;
    let sync_dir = home.join("vigosync");
    if !sync_dir.is_dir() {
        bail!("{} does not exist — nothing to re-encrypt", sync_dir.display());
    }
    let recipients = config::load_recipients(&home)
        .context("reencrypt requires a valid recipients.txt")?;
    let identity = config::load_identity(&home)
        .context("reencrypt needs the identity to decrypt existing ciphertext first")?;

    let mut count = 0usize;
    let mut skipped = 0usize;
    for entry in fs::read_dir(&sync_dir)? {
        let entry = entry?;
        let path = entry.path();
        if !path.is_file() {
            continue;
        }
        let name = match path.file_name().and_then(|n| n.to_str()) {
            Some(n) => n,
            None => continue,
        };
        if name.starts_with('.') {
            continue;
        }

        match crypt::is_age_encrypted(&path) {
            Ok(true) => {
                // Decrypt to tmp, replace with fresh encryption.
                if let Err(e) = reencrypt_one(&path, &identity, &recipients) {
                    eprintln!("skip {}: {}", path.display(), e);
                    skipped += 1;
                    continue;
                }
                count += 1;
            }
            Ok(false) => {
                // Fresh plaintext — encrypt directly.
                if let Err(e) = crypt::encrypt_in_place(&path, &recipients) {
                    eprintln!("skip {}: {}", path.display(), e);
                    skipped += 1;
                    continue;
                }
                count += 1;
            }
            Err(e) => {
                eprintln!("skip {}: {}", path.display(), e);
                skipped += 1;
            }
        }
    }
    eprintln!("re-encrypted {} file(s); skipped {}", count, skipped);
    Ok(())
}

fn reencrypt_one(
    path: &Path,
    identity: &Identity,
    recipients: &[age::x25519::Recipient],
) -> Result<()> {
    let dir = path.parent().context("no parent")?;
    let name = path.file_name().context("no name")?.to_string_lossy().to_string();
    let plain_tmp = dir.join(format!(".{}.vigosync.reenc.tmp", name));

    // Decrypt to temp.
    {
        let mut f = fs::File::create(&plain_tmp)
            .with_context(|| format!("creating {}", plain_tmp.display()))?;
        crypt::decrypt_to_writer(path, identity, &mut f)?;
        f.sync_all().ok();
    }

    // Overwrite path with plaintext (the scanner would've skipped ciphertext,
    // but we want encrypt_in_place to act on the actual plaintext bytes).
    fs::rename(&plain_tmp, path)
        .with_context(|| format!("rename plaintext over ciphertext"))?;

    crypt::encrypt_in_place(path, recipients)
}

// ---------- helpers ----------

/// Resolve the invoking user's home directory. Used by every verb.
fn user_home_for_invocation() -> Result<PathBuf> {
    // Standard environment lookup — `$HOME` wins, falls back to passwd
    // via the platform helper.
    if let Some(h) = std::env::var_os("HOME").map(PathBuf::from) {
        if !h.as_os_str().is_empty() {
            return Ok(h);
        }
    }
    #[cfg(unix)]
    {
        let uid = unsafe { libc::getuid() };
        // Walk /etc/passwd for the uid — avoids pulling in a crate for this.
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

#[cfg(unix)]
fn write_private(path: &Path, content: &str) -> Result<()> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)
        .with_context(|| format!("creating {}", path.display()))?;
    f.write_all(content.as_bytes())?;
    if !content.ends_with('\n') {
        f.write_all(b"\n")?;
    }
    f.sync_all().ok();
    Ok(())
}

#[cfg(not(unix))]
fn write_private(path: &Path, content: &str) -> Result<()> {
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)
        .with_context(|| format!("creating {}", path.display()))?;
    f.write_all(content.as_bytes())?;
    if !content.ends_with('\n') {
        f.write_all(b"\n")?;
    }
    f.sync_all().ok();
    Ok(())
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
