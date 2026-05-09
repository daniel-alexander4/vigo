// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Puddle session helper daemon.
//!
//! Mirrors `crate::swarm::lockbox::session` in shape but holds an
//! Ed25519 [`SigningKey`](ed25519_dalek::SigningKey) instead of an
//! age identity. Spawned by `vigo swarm puddle unlock` and the
//! `join` flow; serves SIGN / STATUS / LOCK over a per-user Unix
//! socket (`$XDG_RUNTIME_DIR/vigo/puddle.sock` on Linux,
//! `$TMPDIR/lockbox-<UID>/puddle.sock` on macOS), 0600,
//! `SO_PEERCRED`-gated.
//!
//! The unwrapped key is held in process memory only. Three
//! independent shutdown triggers — explicit LOCK, parent-PID
//! disappearance, idle timeout (default 60 m) — each scrub the key
//! and exit. The same `~/.vigo-puddle/session.unlocked` sentinel
//! lockbox uses for ADR-012 is mirrored here so sibling
//! subsystems can ask "is this user's puddle identity unlocked?"
//! without speaking the SIGN protocol.

use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use std::process;
use std::time::{Duration, Instant};

use anyhow::{bail, Context, Result};
use base64::engine::general_purpose::STANDARD as B64;
use base64::Engine;
use ed25519_dalek::{Signer, SigningKey};
use tokio::sync::mpsc;
use zeroize::Zeroizing;

// ---------- runtime paths ----------

/// Per-UID runtime dir. Same shape as lockbox's; reusing the
/// `vigo/` parent so a single 0700 dir holds every per-user socket
/// the agent owns.
pub fn runtime_dir() -> Result<PathBuf> {
    let uid = unsafe { libc::getuid() };
    let dir = if let Some(xdg) = std::env::var_os("XDG_RUNTIME_DIR") {
        PathBuf::from(xdg).join("vigo")
    } else if let Some(tmp) = std::env::var_os("TMPDIR") {
        PathBuf::from(tmp).join(format!("lockbox-{}", uid))
    } else {
        PathBuf::from(format!("/tmp/lockbox-{}", uid))
    };
    std::fs::create_dir_all(&dir)
        .with_context(|| format!("creating {}", dir.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o700));
    }
    Ok(dir)
}

pub fn socket_path() -> Result<PathBuf> {
    Ok(runtime_dir()?.join("puddle.sock"))
}

pub fn pidfile_path() -> Result<PathBuf> {
    Ok(runtime_dir()?.join("puddle.pid"))
}

// ---------- client side ----------

pub fn connect() -> Result<UnixStream> {
    let path = socket_path()?;
    UnixStream::connect(&path)
        .with_context(|| format!("connecting to puddle helper at {}", path.display()))
}

/// Ask the running helper to sign `payload`. Returns the raw 64-byte
/// Ed25519 signature.
pub fn request_sign(payload: &[u8]) -> Result<Vec<u8>> {
    let stream = connect()?;
    let (mut reader, mut writer) = stream_halves(stream)?;
    writer
        .write_all(format!("SIGN {}\n", B64.encode(payload)).as_bytes())
        .context("sending SIGN")?;
    writer.flush().ok();
    let mut line = String::new();
    BufReader::new(&mut reader)
        .read_line(&mut line)
        .context("reading SIGN response")?;
    parse_ok_or_err(&line)
}

pub fn request_status() -> Result<String> {
    let stream = connect()?;
    let (mut reader, mut writer) = stream_halves(stream)?;
    writer.write_all(b"STATUS\n").context("sending STATUS")?;
    writer.flush().ok();
    let mut line = String::new();
    BufReader::new(&mut reader)
        .read_line(&mut line)
        .context("reading STATUS response")?;
    Ok(line.trim().to_string())
}

pub fn request_lock() -> Result<()> {
    let stream = match connect() {
        Ok(s) => s,
        Err(_) => {
            // Already locked. Clean up stale socket + pidfile if any.
            let _ = std::fs::remove_file(socket_path()?);
            let _ = std::fs::remove_file(pidfile_path()?);
            return Ok(());
        }
    };
    let (_reader, mut writer) = stream_halves(stream)?;
    let _ = writer.write_all(b"LOCK\n");
    let _ = writer.flush();
    Ok(())
}

fn stream_halves(stream: UnixStream) -> Result<(UnixStream, UnixStream)> {
    stream.set_read_timeout(Some(Duration::from_secs(30))).ok();
    stream.set_write_timeout(Some(Duration::from_secs(30))).ok();
    let reader = stream.try_clone().context("cloning Unix stream")?;
    Ok((reader, stream))
}

fn parse_ok_or_err(line: &str) -> Result<Vec<u8>> {
    let trimmed = line.trim_end_matches('\n');
    if let Some(rest) = trimmed.strip_prefix("OK ") {
        let bytes = B64.decode(rest.trim()).context("decoding OK payload")?;
        Ok(bytes)
    } else if let Some(reason) = trimmed.strip_prefix("ERR ") {
        bail!("helper error: {}", reason)
    } else if trimmed == "OK" {
        Ok(Vec::new())
    } else {
        bail!("unexpected helper response: {}", trimmed)
    }
}

// ---------- spawn the helper ----------

/// Spawn the puddle session helper from the `unlock` / `join`
/// CLI path. Same `setsid` + stdin-piped-secret pattern lockbox
/// uses so the helper outlives its launching shell and the wire
/// secret never lands in argv or env.
pub fn spawn_helper(
    key: &SigningKey,
    parent_pid: u32,
    idle_mins: u32,
    home: &Path,
) -> Result<()> {
    use std::os::unix::process::CommandExt;
    use std::process::{Command, Stdio};

    // Ask any existing helper to lock first so two helpers don't
    // fight over the same socket.
    let _ = request_lock();
    std::thread::sleep(Duration::from_millis(100));

    // Stderr → log file in the runtime dir; same convention as
    // lockbox. Avoids holding the parent shell's stderr open.
    let log_path = runtime_dir()?.join("puddle.log");
    let log_file = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&log_path)
        .with_context(|| format!("opening {}", log_path.display()))?;
    let log_stderr = log_file.try_clone().context("cloning log fd for stderr")?;

    let current_exe = std::env::current_exe().context("resolving current exe")?;
    let mut cmd = Command::new(&current_exe);
    cmd.arg("puddle-session-daemon")
        .arg("--parent-pid")
        .arg(parent_pid.to_string())
        .arg("--idle-mins")
        .arg(idle_mins.to_string())
        .arg("--home")
        .arg(home.as_os_str())
        .stdin(Stdio::piped())
        .stdout(Stdio::from(log_file))
        .stderr(Stdio::from(log_stderr));

    unsafe {
        cmd.pre_exec(|| {
            libc::setsid();
            Ok(())
        });
    }

    let mut child = cmd.spawn().context("spawning puddle session helper")?;
    let mut stdin = child
        .stdin
        .take()
        .context("puddle helper stdin not piped")?;
    // Wire format: `<32 bytes key><newline>`. Bytes go through
    // Zeroizing so we don't leave a lingering plaintext copy in our
    // own process heap after stdin closes.
    let mut payload = Zeroizing::new(Vec::with_capacity(33));
    payload.extend_from_slice(&key.to_bytes());
    payload.push(b'\n');
    stdin
        .write_all(&payload)
        .context("writing key to helper stdin")?;
    drop(stdin);

    // Detach — daemon reaps via parent-PID watchdog after the user's
    // shell exits.
    std::mem::forget(child);

    // Wait for the helper to bind its socket before returning. Without
    // this, callers who immediately try to talk to the helper (notably
    // the lockbox auto-bootstrap inside `cmd_unlock`) race the
    // helper's stdin-read + bind sequence and hit ENOENT on
    // UnixStream::connect. The bind in `daemon_main` is the last
    // synchronous step before the accept loop, so a successful
    // connect() means the helper is ready to handle requests.
    //
    // 5 s budget is generous on every platform we run; the typical
    // wait is one or two 50 ms ticks. If we exceed it the helper
    // either crashed during stdin-read or got stuck pre-bind — either
    // way the operator wants to know rather than have callers fail
    // with the cryptic "No such file or directory" they'd get from a
    // bare connect() against the missing socket.
    let ready_deadline = Instant::now() + Duration::from_secs(5);
    let poll_interval = Duration::from_millis(50);
    let mut attempts: u32 = 0;
    loop {
        attempts += 1;
        if connect().is_ok() {
            if attempts > 1 {
                tracing::debug!(
                    attempts,
                    "puddle helper ready after socket-bind wait"
                );
            }
            return Ok(());
        }
        if Instant::now() >= ready_deadline {
            let log_path = runtime_dir()?.join("puddle.log");
            bail!(
                "puddle helper failed to bind socket within 5s after spawn ({} polls); \
                 helper likely crashed during stdin-read or pre-bind. Check {} for details.",
                attempts,
                log_path.display()
            );
        }
        std::thread::sleep(poll_interval);
    }
}

// ---------- daemon side ----------

pub async fn daemon_main(args: Vec<String>) -> Result<()> {
    let parent_pid = arg_value(&args, "--parent-pid")?.parse::<u32>()?;
    let idle_mins = arg_value(&args, "--idle-mins")
        .unwrap_or_else(|_| "60".into())
        .parse::<u32>()?;
    let home_arg = arg_value(&args, "--home").ok().map(PathBuf::from);

    // Read the 32-byte key from stdin.
    let mut stdin = std::io::stdin();
    let mut buf = Zeroizing::new(Vec::with_capacity(64));
    stdin.read_to_end(&mut buf).context("reading key from stdin")?;
    // Strip a trailing newline (added by spawn_helper); the bytes
    // before the newline are the raw 32-byte secret key.
    let raw: &[u8] = if buf.last() == Some(&b'\n') {
        &buf[..buf.len() - 1]
    } else {
        &buf[..]
    };
    if raw.len() != 32 {
        bail!("session helper: expected 32-byte key on stdin, got {}", raw.len());
    }
    let mut key_bytes = [0u8; 32];
    key_bytes.copy_from_slice(raw);
    let key = SigningKey::from_bytes(&key_bytes);

    let sock_path = socket_path()?;
    let pid_path = pidfile_path()?;
    let _ = std::fs::remove_file(&sock_path);
    let listener = UnixListener::bind(&sock_path)
        .with_context(|| format!("binding {}", sock_path.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(
            &sock_path,
            std::fs::Permissions::from_mode(0o600),
        );
    }
    listener
        .set_nonblocking(true)
        .context("setting listener nonblocking")?;

    std::fs::write(&pid_path, format!("{}\n", process::id())).ok();

    // Drop the session-unlocked sentinel so sibling subsystems can
    // detect lock state by reading the home-rooted file.
    let unlocked_sentinel = home_arg
        .as_ref()
        .map(|h| super::config::session_unlocked_path(h));
    if let Some(path) = unlocked_sentinel.as_ref() {
        if let Some(parent) = path.parent() {
            let _ = std::fs::create_dir_all(parent);
        }
        let _ = std::fs::write(path, format!("{}\n", process::id()));
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(
                path,
                std::fs::Permissions::from_mode(0o600),
            );
        }
    }

    let listener = tokio::net::UnixListener::from_std(listener)
        .context("tokio UnixListener")?;
    let result = run_server(listener, key, parent_pid, idle_mins).await;

    let _ = std::fs::remove_file(&sock_path);
    let _ = std::fs::remove_file(&pid_path);
    if let Some(path) = unlocked_sentinel.as_ref() {
        let _ = std::fs::remove_file(path);
    }
    result
}

async fn run_server(
    listener: tokio::net::UnixListener,
    key: SigningKey,
    parent_pid: u32,
    idle_mins: u32,
) -> Result<()> {
    let my_uid = unsafe { libc::getuid() };
    let idle_total = Duration::from_secs(idle_mins as u64 * 60);
    let (activity_tx, mut activity_rx) = mpsc::unbounded_channel::<()>();
    let (shutdown_tx, mut shutdown_rx) = mpsc::channel::<()>(1);

    tracing::info!(
        parent_pid,
        idle_mins,
        "puddle session helper started"
    );

    // Parent-PID watchdog.
    let watchdog_shutdown = shutdown_tx.clone();
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(Duration::from_secs(10));
        loop {
            interval.tick().await;
            if !pid_alive(parent_pid) {
                tracing::info!(parent_pid, "parent PID gone, shutting down");
                let _ = watchdog_shutdown.send(()).await;
                return;
            }
        }
    });

    // Idle timer.
    let idle_shutdown = shutdown_tx.clone();
    tokio::spawn(async move {
        let mut last_activity = Instant::now();
        loop {
            let elapsed = last_activity.elapsed();
            if elapsed >= idle_total {
                tracing::info!("idle timeout reached, shutting down");
                let _ = idle_shutdown.send(()).await;
                return;
            }
            let sleep_for = idle_total - elapsed;
            tokio::select! {
                _ = tokio::time::sleep(sleep_for) => {}
                got = activity_rx.recv() => {
                    if got.is_some() {
                        last_activity = Instant::now();
                    } else {
                        return;
                    }
                }
            }
        }
    });

    loop {
        tokio::select! {
            _ = shutdown_rx.recv() => return Ok(()),
            accept = listener.accept() => {
                let (stream, _addr) = match accept {
                    Ok(v) => v,
                    Err(e) => {
                        tracing::debug!(error = %e, "accept error");
                        continue;
                    }
                };
                if !peer_uid_matches(&stream, my_uid) {
                    tracing::warn!("rejecting puddle client with mismatched UID");
                    drop(stream);
                    continue;
                }
                let key = key.clone();
                let act = activity_tx.clone();
                let sd = shutdown_tx.clone();
                tokio::spawn(async move {
                    if let Err(e) = handle_client(stream, key, act, sd).await {
                        tracing::debug!(error = %e, "client session error");
                    }
                });
            }
        }
    }
}

async fn handle_client(
    stream: tokio::net::UnixStream,
    key: SigningKey,
    activity: mpsc::UnboundedSender<()>,
    shutdown: mpsc::Sender<()>,
) -> Result<()> {
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    let (reader, mut writer) = stream.into_split();
    let mut br = BufReader::new(reader);
    let mut line = String::new();
    let n = br.read_line(&mut line).await.context("reading request")?;
    if n == 0 {
        return Ok(());
    }
    let line = line.trim_end_matches('\n');

    if line == "LOCK" {
        let _ = writer.write_all(b"OK\n").await;
        let _ = writer.flush().await;
        let _ = shutdown.send(()).await;
        return Ok(());
    }

    if line == "STATUS" {
        let _ = activity.send(());
        let _ = writer.write_all(b"STATUS unlocked=true\n").await;
        return Ok(());
    }

    if line == "PUBKEY" {
        let _ = activity.send(());
        let pk = key.verifying_key();
        let line = format!("OK {}\n", B64.encode(pk.to_bytes()));
        let _ = writer.write_all(line.as_bytes()).await;
        return Ok(());
    }

    if let Some(rest) = line.strip_prefix("SIGN ") {
        let _ = activity.send(());
        let payload = match B64.decode(rest.trim()) {
            Ok(b) => b,
            Err(_) => {
                let _ = writer.write_all(b"ERR bad base64 payload\n").await;
                return Ok(());
            }
        };
        let sig = key.sign(&payload);
        let resp = format!("OK {}\n", B64.encode(sig.to_bytes()));
        let _ = writer.write_all(resp.as_bytes()).await;
        let _ = writer.flush().await;
        return Ok(());
    }

    let _ = writer.write_all(b"ERR unknown verb\n").await;
    Ok(())
}

fn pid_alive(pid: u32) -> bool {
    #[cfg(target_os = "linux")]
    {
        return std::path::Path::new(&format!("/proc/{}", pid)).exists();
    }
    #[cfg(not(target_os = "linux"))]
    {
        let rc = unsafe { libc::kill(pid as libc::pid_t, 0) };
        if rc == 0 {
            return true;
        }
        let err = std::io::Error::last_os_error();
        err.raw_os_error() != Some(libc::ESRCH)
    }
}

fn peer_uid_matches(stream: &tokio::net::UnixStream, our_uid: u32) -> bool {
    #[cfg(target_os = "linux")]
    {
        use std::mem;
        use std::os::fd::AsRawFd;
        let fd = stream.as_raw_fd();
        let mut cred: libc::ucred = unsafe { mem::zeroed() };
        let mut len = mem::size_of::<libc::ucred>() as libc::socklen_t;
        let rc = unsafe {
            libc::getsockopt(
                fd,
                libc::SOL_SOCKET,
                libc::SO_PEERCRED,
                &mut cred as *mut _ as *mut _,
                &mut len,
            )
        };
        if rc != 0 {
            return false;
        }
        return cred.uid == our_uid;
    }
    #[cfg(target_os = "macos")]
    {
        use std::os::fd::AsRawFd;
        let fd = stream.as_raw_fd();
        let mut cred: [u32; 2] = [0; 2];
        let mut len = std::mem::size_of_val(&cred) as libc::socklen_t;
        const SOL_LOCAL: libc::c_int = 0;
        const LOCAL_PEEREID: libc::c_int = 3;
        let rc = unsafe {
            libc::getsockopt(
                fd,
                SOL_LOCAL,
                LOCAL_PEEREID,
                cred.as_mut_ptr() as *mut _,
                &mut len,
            )
        };
        if rc != 0 {
            return false;
        }
        return cred[0] == our_uid;
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    {
        let _ = stream;
        let _ = our_uid;
        false
    }
}

fn arg_value(args: &[String], key: &str) -> Result<String> {
    let mut iter = args.iter();
    while let Some(a) = iter.next() {
        if a == key {
            return iter
                .next()
                .cloned()
                .with_context(|| format!("{} expects a value", key));
        }
    }
    bail!("missing arg {}", key)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_ok_decodes_base64() {
        let line = format!("OK {}\n", B64.encode(b"sig-bytes"));
        let out = parse_ok_or_err(&line).unwrap();
        assert_eq!(out, b"sig-bytes");
    }

    #[test]
    fn parse_err_returns_error() {
        let err = parse_ok_or_err("ERR no key loaded\n").unwrap_err();
        assert!(err.to_string().contains("no key loaded"));
    }

    #[test]
    fn arg_value_finds_key() {
        let args = vec![
            "--parent-pid".into(),
            "42".into(),
            "--idle-mins".into(),
            "60".into(),
        ];
        assert_eq!(arg_value(&args, "--parent-pid").unwrap(), "42");
        assert_eq!(arg_value(&args, "--idle-mins").unwrap(), "60");
        assert!(arg_value(&args, "--missing").is_err());
    }
}
