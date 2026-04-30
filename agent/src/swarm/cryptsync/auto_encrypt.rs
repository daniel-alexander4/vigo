// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! In-place auto-encrypt primitive for plaintext drops in `~<user>/cryptsync/`.
//!
//! Used by both the watcher (root-context, fires on inotify/kqueue
//! event for plaintext content) and `vigo swarm cryptsync unlock`'s
//! sweep (user-context, drains files left plaintext while locked).
//!
//! Steps:
//!
//! 1. Stage to `~<user>/.cryptsync/staging/<uuid>` — root-readable
//!    while we encrypt; never lands in the watched content dir.
//! 2. Encrypt the staged plaintext to current recipients (loaded by
//!    the caller — passed in as a slice).
//! 3. Atomic rename into `~<user>/cryptsync/<filename>`, replacing
//!    the original plaintext file.
//! 4. Chown to the user (no-op when invoked in user context, real
//!    chown when invoked from the daemon).
//!
//! Error classification follows ADR-012 §2:
//!
//! - **Terminal-actionable**: a precondition the user can fix
//!   (recipients empty, disk full, permission denied, source vanished
//!   mid-read). Surface a single specific WARN, leave the plaintext
//!   in place. The next watcher event after the user fixes the cause
//!   retries naturally.
//! - **Terminal-unrecoverable**: the input or local key state is
//!   broken in a way no retry can repair (age library refuses, key
//!   corrupt). Caller renames to `<filename>.<unix_ts>.failed` so the
//!   user sees the artifact and the watcher won't loop.
//! - **Transient**: something in the I/O path that may succeed if we
//!   try again. Caller retries 3× with 100ms / 500ms / 2s backoff.
//!   On exhaustion, treats as unrecoverable (rename to `.failed`).

use std::fs;
use std::io::ErrorKind;
use std::path::{Path, PathBuf};
use std::time::Duration;

use age::x25519::Recipient;
use thiserror::Error;

use super::config;
use super::crypt;

/// Size cap on auto-encrypt. Matches `server::MAX_PUSH_BYTES` so the
/// "can encrypt" and "can transmit" boundaries coincide — never burn
/// CPU on an encrypt that the push handler would reject anyway.
pub const MAX_AUTO_ENCRYPT_BYTES: u64 = super::server::MAX_PUSH_BYTES as u64;

/// Suffix appended (with a unix timestamp) when a file fails
/// unrecoverably. Filtered out by the watcher so it never re-enters
/// the encrypt pipeline.
pub const FAILED_SUFFIX: &str = ".failed";

/// Three-class encrypt outcome. The watcher consumes this and
/// translates each class into a specific log line + filesystem action
/// per ADR-012 §2 / §5.
#[derive(Debug, Error)]
pub enum AutoEncryptError {
    /// User-fixable precondition. Leave plaintext in place; do not
    /// rename. The carried message names the specific cause.
    #[error("terminal: {0}")]
    Terminal(String),

    /// Locally-broken state with no retry path. Caller renames the
    /// source file to `<filename>.<unix_ts>.failed`.
    #[error("unrecoverable: {0}")]
    Unrecoverable(String),

    /// Transient I/O — caller may retry per the backoff schedule.
    /// Final exhaustion converts to `Unrecoverable` at the call site.
    #[error("transient: {0}")]
    Transient(String),
}

/// Per ADR-012: 3 attempts on transient errors with 100ms / 500ms / 2s
/// backoffs between attempts. Total wall-clock <3s before the error
/// converts to unrecoverable.
pub const RETRY_BACKOFFS: &[Duration] = &[
    Duration::from_millis(100),
    Duration::from_millis(500),
    Duration::from_secs(2),
];

/// Encrypt `~<user>/cryptsync/<filename>` in place, replacing the
/// plaintext file with age ciphertext. `recipients` must already
/// have been loaded by the caller (so the recipient resolution path
/// stays in one place — config::load_recipients today, fleet-aware
/// resolution under ADR-016 tomorrow).
///
/// `username` is the local Unix user that owns the home dir. On a
/// successful rename, the resulting ciphertext file is chowned to
/// `username` — idempotent when the caller already runs in user
/// context.
///
/// Returns:
/// - `Ok(size_bytes)` on success — caller logs and lets the natural
///   inotify event from the rename trigger the push pipeline.
/// - `Err(AutoEncryptError)` on failure, classified per ADR-012 §2.
pub fn encrypt_in_place(
    home: &Path,
    username: &str,
    filename: &str,
    recipients: &[Recipient],
) -> Result<u64, AutoEncryptError> {
    if recipients.is_empty() {
        return Err(AutoEncryptError::Terminal(
            "recipients.txt is empty (no peers known to encrypt to)".into(),
        ));
    }

    let content_dir = config::content_dir(home);
    let src = content_dir.join(filename);

    let meta = fs::metadata(&src).map_err(|e| classify_io("stat source", &e))?;
    if !meta.is_file() {
        return Err(AutoEncryptError::Terminal(format!(
            "{} is not a regular file",
            src.display()
        )));
    }
    let size = meta.len();
    if size > MAX_AUTO_ENCRYPT_BYTES {
        return Err(AutoEncryptError::Terminal(format!(
            "size={} exceeds {} push cap",
            size, MAX_AUTO_ENCRYPT_BYTES
        )));
    }

    let staging_dir = staging_dir_for(home);
    if let Err(e) = ensure_staging_dir(&staging_dir, username) {
        return Err(classify_io("create staging dir", &e));
    }
    let token = uuid::Uuid::new_v4().to_string();
    let staged = staging_dir.join(&token);

    let result = crypt::encrypt_file(&src, &staged, recipients).map_err(classify_age_error);
    if let Err(e) = result {
        let _ = fs::remove_file(&staged);
        return Err(e);
    }

    // Mode 0600 + atomic rename into ~<user>/cryptsync/<filename>,
    // then chown the result to the user. finalize_user_ciphertext
    // does all three steps. On failure the staged tempfile is
    // best-effort cleaned.
    if let Err(e) = super::finalize_user_ciphertext(&staged, &src, username) {
        let _ = fs::remove_file(&staged);
        return Err(classify_anyhow("finalize ciphertext", e));
    }

    Ok(size)
}

/// Public helper: rename a file to `<original>.<unix_ts>.failed` when
/// the encrypt outcome is unrecoverable. Watcher uses this to land
/// the artifact under a name `resolve_event` filters out, so the
/// failure doesn't loop.
pub fn rename_to_failed(home: &Path, filename: &str, now_ts: i64) -> std::io::Result<PathBuf> {
    let content_dir = config::content_dir(home);
    let src = content_dir.join(filename);
    let dst = content_dir.join(format!("{}.{}{}", filename, now_ts, FAILED_SUFFIX));
    fs::rename(&src, &dst)?;
    Ok(dst)
}

/// True if `name` was produced by `rename_to_failed`. Watcher's event
/// filter calls this to short-circuit failed-rename inotify events.
/// The suffix shape is `<orig>.<unix_ts>.failed` — `<unix_ts>` is a
/// positive integer so the digit-then-suffix tail is unambiguous.
pub fn is_failed_artifact_name(name: &str) -> bool {
    let stem = match name.strip_suffix(FAILED_SUFFIX) {
        Some(s) => s,
        None => return false,
    };
    // Last `.`-separated token must be all digits (the unix timestamp
    // suffix). Bare `<orig>.failed` is *not* one of ours.
    let ts = match stem.rsplit('.').next() {
        Some(t) if !t.is_empty() => t,
        _ => return false,
    };
    ts.chars().all(|c| c.is_ascii_digit()) && ts.len() < stem.len()
}

/// Staging directory for in-flight encrypts. Lives under the user's
/// dot dir so it inherits the same ACLs and survives reboots cleanly
/// (any orphan stagers from a crash are 0600 user-readable and can be
/// rm'd by the user). Watcher reconciles `~/cryptsync/`, not
/// `~/.cryptsync/staging/`, so contents here never trigger events.
pub fn staging_dir_for(home: &Path) -> PathBuf {
    config::dot_dir(home).join("staging")
}

fn ensure_staging_dir(dir: &Path, username: &str) -> std::io::Result<()> {
    if !dir.is_dir() {
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            fs::DirBuilder::new()
                .recursive(true)
                .mode(0o700)
                .create(dir)?;
        }
        #[cfg(not(unix))]
        {
            fs::create_dir_all(dir)?;
        }
    }
    #[cfg(unix)]
    {
        // Best-effort chown so files we drop here end up user-owned
        // when the directory is touched by root.
        let _ = super::chown_to_user(dir, username);
    }
    #[cfg(not(unix))]
    {
        let _ = username;
    }
    Ok(())
}

/// Map a fresh `std::io::Error` from a file-staging step into the
/// three-class taxonomy. Specific kinds map to terminal-actionable
/// (the user can fix the cause); everything else is transient.
fn classify_io(stage: &str, err: &std::io::Error) -> AutoEncryptError {
    match err.kind() {
        ErrorKind::NotFound => {
            AutoEncryptError::Terminal(format!("{}: source vanished mid-read", stage))
        }
        ErrorKind::PermissionDenied => {
            AutoEncryptError::Terminal(format!("{}: permission denied", stage))
        }
        // ErrorKind::StorageFull is unstable; match raw_os_error ENOSPC
        // and the generic "no space" Other-kind on macOS.
        _ if err.raw_os_error() == Some(libc::ENOSPC) => {
            AutoEncryptError::Terminal(format!("{}: disk full", stage))
        }
        _ => AutoEncryptError::Transient(format!("{}: {}", stage, err)),
    }
}

/// Map an anyhow chain back into the taxonomy. Walk the source chain
/// looking for an `std::io::Error`, then defer to `classify_io`. Falls
/// back to transient when no I/O cause is found.
fn classify_anyhow(stage: &str, err: anyhow::Error) -> AutoEncryptError {
    if let Some(io_err) = err.downcast_ref::<std::io::Error>() {
        return classify_io(stage, io_err);
    }
    for cause in err.chain() {
        if let Some(io_err) = cause.downcast_ref::<std::io::Error>() {
            return classify_io(stage, io_err);
        }
    }
    AutoEncryptError::Transient(format!("{}: {}", stage, err))
}

/// Map an error from `crypt::encrypt_file`. The age library produces
/// errors via the `age::Encryptor` path that don't surface as
/// `io::Error` — those are unrecoverable (input cannot be encoded /
/// recipient material refused). I/O during the streaming copy still
/// surfaces as classifiable I/O errors.
fn classify_age_error(err: anyhow::Error) -> AutoEncryptError {
    // I/O errors anywhere in the chain → io classification.
    for cause in err.chain() {
        if let Some(io_err) = cause.downcast_ref::<std::io::Error>() {
            return classify_io("encrypt_file", io_err);
        }
    }
    let msg = err.to_string();
    // Recognize the no-recipients precondition specifically — caller
    // may have raced a recipients.txt regen between load and encrypt.
    if msg.contains("no recipients") {
        return AutoEncryptError::Terminal("recipients.txt empty between load and encrypt".into());
    }
    // Anything else from age is unrecoverable — the encrypt would
    // fail the same way on retry.
    AutoEncryptError::Unrecoverable(format!("age encrypt failed: {}", msg))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn failed_artifact_name_recognizes_our_pattern() {
        assert!(is_failed_artifact_name("foo.txt.20260429.failed"));
        assert!(is_failed_artifact_name("a.b.c.1234567890.failed"));
    }

    #[test]
    fn failed_artifact_name_rejects_bare_failed_suffix() {
        // User-created `*.failed` files (without a unix-timestamp
        // segment) must not be filtered — the user might genuinely
        // want to sync something they named `report.failed`.
        assert!(!is_failed_artifact_name("report.failed"));
        assert!(!is_failed_artifact_name(".failed"));
    }

    #[test]
    fn failed_artifact_name_rejects_other_files() {
        assert!(!is_failed_artifact_name("normal.txt"));
        assert!(!is_failed_artifact_name("foo.bar.baz"));
        assert!(!is_failed_artifact_name("foo.notnumber.failed"));
        assert!(!is_failed_artifact_name(""));
    }

    #[test]
    fn classify_io_terminal_on_known_kinds() {
        let nf = std::io::Error::from(ErrorKind::NotFound);
        match classify_io("stat", &nf) {
            AutoEncryptError::Terminal(m) => assert!(m.contains("source vanished")),
            other => panic!("expected Terminal, got {:?}", other),
        }
        let pd = std::io::Error::from(ErrorKind::PermissionDenied);
        match classify_io("open", &pd) {
            AutoEncryptError::Terminal(_) => {}
            other => panic!("expected Terminal, got {:?}", other),
        }
        let nospace = std::io::Error::from_raw_os_error(libc::ENOSPC);
        match classify_io("write", &nospace) {
            AutoEncryptError::Terminal(m) => assert!(m.contains("disk full")),
            other => panic!("expected Terminal for ENOSPC, got {:?}", other),
        }
    }

    #[test]
    fn classify_io_transient_on_unknown_kinds() {
        let other = std::io::Error::new(ErrorKind::Other, "weird");
        match classify_io("read", &other) {
            AutoEncryptError::Transient(_) => {}
            unexpected => panic!("expected Transient, got {:?}", unexpected),
        }
    }

    #[test]
    fn classify_anyhow_walks_to_io_cause() {
        let inner = std::io::Error::from(ErrorKind::PermissionDenied);
        let wrapped: anyhow::Error =
            anyhow::Error::new(inner).context("outer context");
        match classify_anyhow("rename", wrapped) {
            AutoEncryptError::Terminal(m) => assert!(m.contains("permission denied")),
            other => panic!("expected Terminal via chain walk, got {:?}", other),
        }
    }

    #[test]
    fn classify_age_error_unrecoverable_on_non_io() {
        let err = anyhow::anyhow!("age writer finish failed: input rejected");
        match classify_age_error(err) {
            AutoEncryptError::Unrecoverable(_) => {}
            other => panic!("expected Unrecoverable, got {:?}", other),
        }
    }

    #[test]
    fn classify_age_error_terminal_on_no_recipients() {
        let err = anyhow::anyhow!("encrypt_file called with no recipients");
        match classify_age_error(err) {
            AutoEncryptError::Terminal(_) => {}
            other => panic!("expected Terminal for no-recipients, got {:?}", other),
        }
    }

    #[test]
    fn encrypt_in_place_round_trips() {
        use age::x25519::Identity;
        let dir = tempfile::tempdir().unwrap();
        let home = dir.path();
        std::fs::create_dir_all(config::content_dir(home)).unwrap();
        std::fs::create_dir_all(config::dot_dir(home)).unwrap();

        let id = Identity::generate();
        let recipients = vec![id.to_public()];

        let plaintext = b"hello plaintext world";
        let plain_path = config::content_dir(home).join("note.txt");
        std::fs::write(&plain_path, plaintext).unwrap();

        // Use the invoking user's name so chown is a no-op (we're
        // running as ourselves in the test, not root).
        let username = std::env::var("USER").unwrap_or_else(|_| "nobody".into());
        let size = encrypt_in_place(home, &username, "note.txt", &recipients)
            .expect("encrypt_in_place should succeed");
        assert_eq!(size, plaintext.len() as u64);

        // After encrypt, the file at the original path must be age
        // ciphertext.
        let after = std::fs::read(&plain_path).unwrap();
        assert!(after.starts_with(b"age-encryption.org/v1\n"));

        // Round-trip verifies it decrypts back to the plaintext.
        let mut out = Vec::new();
        crypt::decrypt_to_writer(&plain_path, &id, &mut out).unwrap();
        assert_eq!(out, plaintext);
    }

    #[test]
    fn encrypt_in_place_terminal_on_empty_recipients() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(config::content_dir(dir.path())).unwrap();
        std::fs::write(config::content_dir(dir.path()).join("a"), b"x").unwrap();
        let r = encrypt_in_place(dir.path(), "nobody", "a", &[]);
        match r {
            Err(AutoEncryptError::Terminal(m)) => assert!(m.contains("recipients")),
            other => panic!("expected Terminal, got {:?}", other),
        }
    }

    #[test]
    fn encrypt_in_place_terminal_on_oversize() {
        // Stub: we don't want to allocate 1 GiB in tests. Instead,
        // verify the size check by stat'ing a sparse file. Skip
        // gracefully when sparse files aren't supported (tmpfs in
        // some CI envs).
        use age::x25519::Identity;
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(config::content_dir(dir.path())).unwrap();
        std::fs::create_dir_all(config::dot_dir(dir.path())).unwrap();
        let path = config::content_dir(dir.path()).join("big");
        let f = std::fs::File::create(&path).unwrap();
        // Set length one byte over the cap. seek+write 0 byte at end
        // to create a sparse file.
        if f.set_len(MAX_AUTO_ENCRYPT_BYTES + 1).is_err() {
            return;
        }
        drop(f);
        let id = Identity::generate();
        let username = std::env::var("USER").unwrap_or_else(|_| "nobody".into());
        let r = encrypt_in_place(dir.path(), &username, "big", &[id.to_public()]);
        match r {
            Err(AutoEncryptError::Terminal(m)) => assert!(m.contains("push cap")),
            other => panic!("expected Terminal for oversize, got {:?}", other),
        }
    }
}
